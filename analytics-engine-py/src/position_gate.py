"""Single-position gate: one live fill per (match, market) until the match
settles. Prevents the engine from re-sending an identical order every frame
while a market is already exposed; the FULLTIME frame resets all of a match's
positions so the next cycle can trade again.
"""
from __future__ import annotations


class PositionGate:
    """Tracks open positions keyed by (match_id, market_id)."""

    def __init__(self) -> None:
        self._open: dict[tuple[str, str], str] = {}

    def try_acquire(self, match_id: str, market_id: str, side: str) -> bool:
        """Claim a position. Returns False if one is already open."""
        key = (match_id, market_id)
        if key in self._open:
            return False
        self._open[key] = side
        return True

    def release(self, match_id: str) -> None:
        """Close every position belonging to a match (FULLTIME boundary)."""
        doomed = [key for key in self._open if key[0] == match_id]
        for key in doomed:
            del self._open[key]

    def has(self, match_id: str, market_id: str) -> bool:
        return (match_id, market_id) in self._open

    def __len__(self) -> int:
        return len(self._open)