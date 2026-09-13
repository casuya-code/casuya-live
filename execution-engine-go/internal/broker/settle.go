package broker

import (
	"math"
	"time"
)

// Fill is a confirmed order placed at the bookmaker.
type Fill struct {
	OrderID string
	Market  string
	Side    string
	Odds    float64
	Stake   float64
	Cycle   int64
	FillAt  time.Time
}

// SettlementOrder is the bookmaker's record of a filled order, ready to be
// graded against the final score.
type SettlementOrder struct {
	OrderID string  `json:"order_id"`
	Market  string  `json:"market"`
	Side    string  `json:"side"`
	Odds    float64 `json:"odds"`
	Stake   float64 `json:"stake"`
}

// Settlement grades a whole match's fills against its final score.
type Settlement struct {
	Type       string            `json:"type"`
	Cycle      int64             `json:"cycle"`
	MatchID    string            `json:"match_id"`
	FinalScore Score             `json:"final_score"`
	Orders     []SettlementOrder `json:"orders"`
}

// Score is the full-time result used to grade the fills.
type Score struct {
	Home float64 `json:"home"`
	Away float64 `json:"away"`
}

// SettledOrder carries the computed result and PnL for one fill.
type SettledOrder struct {
	SettlementOrder
	Result string  `json:"result"`
	Pnl    float64 `json:"pnl"`
}

// SessionTotals is the running bankroll view across all settled matches.
type SessionTotals struct {
	Net  float64 `json:"net"`
	Won  int     `json:"won"`
	Lost int     `json:"lost"`
}

// PnlSnapshot is the operator-facing payload published on the PnL channel.
type PnlSnapshot struct {
	Type       string         `json:"type"`
	Cycle      int64          `json:"cycle"`
	MatchID    string         `json:"match_id"`
	FinalScore Score          `json:"final_score"`
	Settled    []SettledOrder `json:"settled"`
	Session    SessionTotals  `json:"session"`
}

// outcomeFor resolves a 1X2 side against the final score.
func outcomeFor(side string, home, away float64) string {
	switch side {
	case "home":
		if home > away {
			return "won"
		}
		if home < away {
			return "lost"
		}
		return "void"
	case "away":
		if away > home {
			return "won"
		}
		if away < home {
			return "lost"
		}
		return "void"
	case "draw":
		if home == away {
			return "won"
		}
		return "lost"
	default:
		return "void"
	}
}

// pnlFor converts a graded result into a flat PnL, rounded to the cent.
// Voids return no movement.
func pnlFor(result string, stake, odds float64) float64 {
	var pnl float64
	switch result {
	case "won":
		pnl = stake * (odds - 1)
	case "lost":
		pnl = -stake
	}
	return math.Round(pnl*100) / 100
}

// settleOrders grades the settlement's fills and returns the settled view.
func settleOrders(orders []SettlementOrder, home, away float64) []SettledOrder {
	settled := make([]SettledOrder, 0, len(orders))
	for _, o := range orders {
		result := outcomeFor(o.Side, home, away)
		settled = append(settled, SettledOrder{
			SettlementOrder: o,
			Result:          result,
			Pnl:             pnlFor(result, o.Stake, o.Odds),
		})
	}
	return settled
}