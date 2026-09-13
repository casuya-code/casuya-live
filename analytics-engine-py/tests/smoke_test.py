"""Smoke test: drive the real pipeline with the sample vendor frame."""
import base64
import json
import os
import sys
import threading
from http.server import BaseHTTPRequestHandler, HTTPServer
from types import SimpleNamespace

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "src"))

from feature_study import FeatureStudy
from filter_engine import FilterEngine, sign_payload

FRAME = os.path.join(os.path.dirname(__file__), "..", "samples", "vendor_frame.json")
KEY = "test-internal-secret"

with open(FRAME, encoding="utf-8") as fh:
    frame = json.load(fh)

study = FeatureStudy()

# --- multi-market frames yield one snapshot per market (no silent drops) ---
multi = json.loads(json.dumps(frame))
multi["markets"]["1H_1X2"] = {
    "market_id": "1H_1X2",
    "odds": {"home": 6.0, "draw": 4.0, "away": 1.8},
    "updated_at": 1786600700,
}
snaps = study.observe_many(multi)
assert len(snaps) == 2, f"expected 2 snapshots, got {len(snaps)}"
assert {s.market_id for s in snaps} == {"2H_1X2", "1H_1X2"}, "market_id must be the dict key"
print(f"observe_many: OK ({len(snaps)} snapshots, market_ids intact)")

# --- Go-shaped frame (nested score) is accepted too ---
go_frame = {
    "match_id": "go-1",
    "sport": "football",
    "league": "",
    "home_team": "A",
    "away_team": "B",
    "kickoff": 1786600000,
    "clock": "31",
    "score": {"home": 0, "away": 1},
    "markets": {
        "2H_1X2": {
            "market_id": "2H_1X2",
            "odds": {"home": 2.1, "draw": 3.4, "away": 5.25},
            "updated_at": 1786600700,
        }
    },
}
snap_go = study.observe(go_frame)
assert snap_go is not None and (snap_go.score_home, snap_go.score_away) == (0, 1)
print("nested Go frame: OK")

# --- boundary clocks (any case) are skipped, FULLTIME settles per market ---
assert study.observe_many({**go_frame, "clock": "fulltime"}) == []
settled = study.collect_settled({**go_frame, "clock": "FULLTIME"})
assert len(settled) == 1 and settled[0].market_id == "2H_1X2", "settled market_id must be the market key"
print("boundary/settle: OK")

snapshot = study.observe(frame)
assert snapshot is not None, "study window rejected valid frame"
print(f"momentum: danger={snapshot.dangerous_attacks} shot_acc={study.shot_accuracy(snapshot):.2f} div={study.divergence(snapshot):.2f}")

snapshot.features = study.feature_vector(snapshot)
print(f"features: {snapshot.features}")
assert abs(snapshot.features["time_pressure"] - 31 / 90) < 1e-9, "plain-minute clock must parse"

flt = FilterEngine(base_url="http://127.0.0.1:9", signer_key=KEY)
verdict = flt.evaluate(snapshot)
print(f"verdict: execute={verdict.should_execute} side={verdict.side} odds={verdict.odds} true={verdict.true_prob} implied={verdict.implied_prob}")
assert verdict.should_execute, "expected positive-EV trigger"
assert verdict.side == "away", "away (high odds + home momentum) should carry the edge"

# --- envelope must be Go-compatible: base64url with NO padding in either half ---
envelope = verdict.to_signed_payload(KEY)
body_b64, sig = envelope.split(".")
assert "=" not in envelope, "RawURLEncoding rejects padding"
body = json.loads(base64.urlsafe_b64decode(body_b64 + "==="))
assert sign_payload(body, KEY) == sig, "HMAC round-trip failed"
print("hmac round-trip (unpadded, Go-compatible): OK")

# --- sub-floor guard ---
class CheapSnap:
    match_id = "m-low"
    market_id = "2H_1X2"
    odds = {"home": 1.5, "draw": 2.0, "away": 2.4}
    features = snapshot.features


low = flt.evaluate(CheapSnap())
assert not low.should_execute, "sub-floor odds must not trigger"
print(f"sub-floor guard: OK ({low.reasons[0]})")

# --- dispatch must send the auth header the Go server requires ---
seen = {}


class Handler(BaseHTTPRequestHandler):
    def do_POST(self):
        length = int(self.headers.get("Content-Length", 0))
        seen["token"] = self.headers.get("X-Internal-Token")
        seen["body"] = self.rfile.read(length)
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(b'{"status":"filled"}')

    def log_message(self, *a):
        pass


srv = HTTPServer(("127.0.0.1", 0), Handler)
threading.Thread(target=srv.serve_forever, daemon=True).start()
flt2 = FilterEngine(base_url=f"http://127.0.0.1:{srv.server_port}", signer_key=KEY)
res = flt2.dispatch(verdict)
srv.shutdown()
assert res.get("status") == "filled", res
assert seen.get("token") == KEY, "X-Internal-Token header missing"
print("dispatch auth header: OK")

# --- position gate: one fill per (match, market) until FULLTIME ---
from position_gate import PositionGate

gate = PositionGate()
assert gate.try_acquire("m1", "2H_1X2", "away") is True
assert gate.try_acquire("m1", "2H_1X2", "home") is False, "same market re-acquired"
assert gate.try_acquire("m1", "1H_1X2", "draw") is True, "distinct market blocked"
assert len(gate) == 2
gate.release("m1")
assert len(gate) == 0, "FULLTIME did not release positions"
assert gate.try_acquire("m1", "2H_1X2", "away") is True, "cycle re-open blocked"
print("position gate: OK")

# --- diagnostics: WON silent, LOST emits, predicted_side preferred ---
from diagnostics import DiagnosticsEngine

diag = DiagnosticsEngine()
won = SimpleNamespace(final_home=1, final_away=0, closing_odds={"home": 5.25, "draw": 3.4, "away": 2.1}, market_id="2H_1X2", match_id="m1")
lost = SimpleNamespace(final_home=0, final_away=2, closing_odds={"home": 5.25, "draw": 3.4, "away": 2.1}, market_id="2H_1X2", match_id="m2")
assert diag.audit(won) is None
rec = diag.audit(lost)
assert rec is not None and rec.drift_score > 0
lost_home = SimpleNamespace(final_home=2, final_away=0, closing_odds={"home": 1.5, "draw": 4.0, "away": 7.0}, market_id="2H_1X2", match_id="m3", predicted_side="away")
rec3 = diag.audit(lost_home)
assert rec3 is not None and rec3.predicted_side == "away", "must use the actual bet side"
import dataclasses

json.dumps(dataclasses.asdict(rec3), sort_keys=True)  # broker publishes this
print(f"diagnostics: won=silent lost=drift {rec.drift_score} recs={len(rec.recommendations)} side={rec3.predicted_side}")

print("SMOKE TEST PASSED")