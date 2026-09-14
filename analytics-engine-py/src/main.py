"""CASUYA-LIVE analytics engine: real-time event broker listener.

Runs a persistent Redis Pub/Sub subscription and wakes evaluation worker
tasks the instant a new validated frame arrives from data-ingestion-go.
"""
from __future__ import annotations

import asyncio
import dataclasses
import json
import logging
import os
from datetime import datetime, timezone

import redis.asyncio as redis

from feature_study import BASE_DIR, FeatureStudy
from filter_engine import FilterEngine
from diagnostics import DiagnosticsEngine, DIAG_CHANNEL
from position_gate import PositionGate

LOG = logging.getLogger("casuya.analytics")

CHANNEL_IN = os.getenv("ANALYTICS_IN_CHANNEL", "matches:live")
CHANNEL_OUT = os.getenv("EXECUTION_OUT_CHANNEL", "execution:commands")


class AnalyticsBroker:
    """Fan-out broker that routes match frames to independent workers."""

    def __init__(self) -> None:
        self.redis_url = os.getenv("REDIS_URL")
        if not self.redis_url:
            raise RuntimeError("REDIS_URL is required")
        self.study = FeatureStudy()
        self.filter = FilterEngine(
            base_url=os.getenv("EXECUTION_SERVICE_URL", "http://execution-engine:8080"),
        )
        self.diagnostics = DiagnosticsEngine()
        self.gate = PositionGate()
        self.gate_lock = asyncio.Lock()
        self.frames: asyncio.Queue = asyncio.Queue(maxsize=4096)
        self.dropped = 0

    def _mirror_model_hash_fields(self) -> dict[str, str] | None:
        """Re-publish calibration:model from the persisted weights file.

        Redis restarts wipe the calibrator's hash; reloading it from
        weights.json at startup keeps the dashboard model card alive without
        requiring an offline calibrate run.
        """
        try:
            weights_file = BASE_DIR / "models" / "calibration" / "weights.json"
            if not weights_file.exists():
                return None
            data = json.loads(weights_file.read_text(encoding="utf-8"))
            if not isinstance(data.get("weights"), list):
                return None
            mapping: dict[str, str] = {"status": "adopted"}
            mapped_keys = (
                "generated_at", "features", "train_rows", "val_rows", "bias",
                "l2", "train_accuracy", "train_brier", "val_accuracy",
                "val_brier", "val_auc", "baseline_logloss", "model_logloss",
            )
            for key in mapped_keys:
                if key in data:
                    mapping[key] = (
                        data[key] if isinstance(data[key], str) else json.dumps(data[key])
                    )
            mapping["weights"] = "[" + ",".join(
                f"{round(w, 6)}" for w in data["weights"]
            ) + "]"
            mapping["rows"] = str(
                (data.get("train_rows", 0) or 0) + (data.get("val_rows", 0) or 0)
            )
            return mapping
        except Exception:
            LOG.exception("unable to load weights.json for model mirror")
            return None

    async def _mirror_model_hash(self, r: redis.Redis) -> None:
        mapping = self._mirror_model_hash_fields()
        if not mapping:
            return
        try:
            await r.hset("calibration:model", mapping=mapping)
            await r.set(
                "calibration:heartbeat",
                datetime.now(timezone.utc).isoformat(timespec="seconds"),
                ex=3600,
            )
            await r.expire("calibration:model", 7 * 86400)
            LOG.info("mirrored calibration:model from weights.json (adopted)")
        except Exception as exc:  # Redis offline must not abort startup
            LOG.warning("model mirror skipped: %s", exc)

    async def run(self) -> None:
        r = redis.from_url(self.redis_url, decode_responses=True)
        await self._mirror_model_hash(r)
        pubsub = r.pubsub()
        await pubsub.subscribe(CHANNEL_IN)
        LOG.info("subscribed to %s", CHANNEL_IN)

        workers = [asyncio.create_task(self._worker(i)) for i in range(4)]
        try:
            async for message in pubsub.listen():
                if message.get("type") != "message":
                    continue
                raw = message.get("data")
                try:
                    frame = json.loads(raw) if isinstance(raw, str) else raw
                except (json.JSONDecodeError, TypeError) as exc:
                    LOG.warning("dropping unparsable frame: %s", exc)
                    continue
                if not isinstance(frame, dict):
                    LOG.warning("dropping non-object frame of type %s", type(frame))
                    continue
                try:
                    self.frames.put_nowait(frame)
                except asyncio.QueueFull:
                    # Shed load instead of back-pressuring the pub/sub loop:
                    # a stalled listen() misses heartbeats and disconnects.
                    self.dropped += 1
                    LOG.warning("frame queue full; dropped %d total", self.dropped)
        finally:
            await pubsub.unsubscribe(CHANNEL_IN)
            for w in workers:
                w.cancel()
            await r.aclose()

    async def _worker(self, worker_id: int) -> None:
        async with redis.from_url(self.redis_url, decode_responses=True) as r:
            while True:
                frame = await self.frames.get()
                try:
                    # Settle FIRST: FULLTIME frames are boundary markers that
                    # observe() deliberately skips, so auditing must precede
                    # the momentum path or LOST bets are never diagnosed.
                    for entry in self.study.collect_settled(frame):
                        record = self.diagnostics.audit(entry)
                        if record is not None:
                            await r.publish(
                                DIAG_CHANNEL,
                                json.dumps(
                                    dataclasses.asdict(record), sort_keys=True
                                ),
                            )
                    # A FULLTIME frame closes the match's cycle: any exposed
                    # market is settled, so the next cycle may trade it again.
                    if str(frame.get("clock", "")).upper() == "FULLTIME":
                        match_id = frame.get("match_id")
                        if match_id:
                            async with self.gate_lock:
                                self.gate.release(match_id)
                    snapshots = self.study.observe_many(frame)
                    for snapshot in snapshots:
                        snapshot.features = self.study.feature_vector(snapshot)
                        verdict = self.filter.evaluate(snapshot)
                        if verdict.should_execute:
                            # One live position per market: hold the trigger
                            # while the same market is already exposed.
                            async with self.gate_lock:
                                acquired = self.gate.try_acquire(
                                    snapshot.match_id, verdict.market_id, verdict.side
                                )
                            if not acquired:
                                LOG.info(
                                    "[worker %d] gated %s/%s: position already open",
                                    worker_id,
                                    snapshot.match_id,
                                    verdict.market_id,
                                )
                                continue
                            await r.publish(
                                CHANNEL_OUT,
                                verdict.to_signed_payload(self.filter.signer_key),
                            )
                            LOG.info(
                                "[worker %d] execution payload emitted for %s (%s)",
                                worker_id,
                                snapshot.match_id,
                                verdict.market_id,
                            )
                except Exception as exc:  # never kill the listener loop
                    LOG.exception("[worker %d] frame error: %s", worker_id, exc)
                finally:
                    self.frames.task_done()


def main() -> None:
    logging.basicConfig(
        level=logging.INFO,
        format="%(asctime)s %(name)s %(levelname)s %(message)s",
    )
    asyncio.run(AnalyticsBroker().run())


if __name__ == "__main__":
    main()