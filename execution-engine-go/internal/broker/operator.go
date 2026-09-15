package broker

import (
	"context"
	"encoding/json"
	"log"
	"os"

	"github.com/redis/go-redis/v9"
)

// OperatorHashDefault is the Redis hash keyed by order_id recording manual
// bookmaker placements made by the human operator (operator-assisted live).
const OperatorHashDefault = "operator:placements"

// OperatorStreamDefault is the append-only log of operator placements,
// rendered newest-first in the operator desk.
const OperatorStreamDefault = "execution:operator_placements"

// PlacementChannelDefault broadcasts operator placement events to dashboards.
const PlacementChannelDefault = "operator:placements"

// OperatorHashName returns the configured placements hash key.
func OperatorHashName() string {
	if v := os.Getenv("OPERATOR_HASH"); v != "" {
		return v
	}
	return OperatorHashDefault
}

// PlacementChannel returns the configured operator placement pub/sub channel.
func PlacementChannel() string {
	if v := os.Getenv("OPERATOR_CHANNEL"); v != "" {
		return v
	}
	return PlacementChannelDefault
}

// Placement is the operator's record that a signal was manually placed on a
// real bookmaker (e.g. Helabet). It is advisory metadata for the operator desk;
// paper grading remains the authoritative PnL validation source.
type Placement struct {
	OrderID  string  `json:"order_id"`
	MatchID  string  `json:"match_id"`
	Market   string  `json:"market"`
	Side     string  `json:"side"`
	Odds     float64 `json:"odds"`
	Stake    float64 `json:"stake"`
	Book     string  `json:"book"`
	Status   string  `json:"status"`
	Note     string  `json:"note"`
	PlacedAt int64   `json:"placed_at"`
}

// RecordPlacement indexes the placement in the lookup hash, appends it to the
// operator ledger stream, and broadcasts it so live dashboards can update.
func RecordPlacement(ctx context.Context, rdb *redis.Client, p Placement) error {
	if rdb == nil {
		return nil
	}
	payload, err := json.Marshal(p)
	if err != nil {
		return err
	}
	pipe := rdb.Pipeline()
	pipe.HSet(ctx, OperatorHashName(), p.OrderID, payload)
	pipe.XAdd(ctx, &redis.XAddArgs{
		Stream: OperatorStreamDefault,
		MaxLen: 1000,
		Approx: true,
		Values: map[string]any{
			"order_id":  p.OrderID,
			"match_id":  p.MatchID,
			"market":    p.Market,
			"side":      p.Side,
			"odds":      p.Odds,
			"stake":     p.Stake,
			"book":      p.Book,
			"status":    p.Status,
			"note":      p.Note,
			"placed_at": p.PlacedAt,
		},
	})
	pipe.Publish(ctx, PlacementChannel(), payload)
	if _, err := pipe.Exec(ctx); err != nil {
		log.Printf("[operator] placement record failed: %v", err)
		return err
	}
	log.Printf("[operator] %s %s placed on %s (%s)", p.OrderID, p.Side, p.Book, p.Status)
	return nil
}

// PlacementFromMap rebuilds a Placement from stream field values.
func PlacementFromMap(m map[string]any) Placement {
	odds, _ := m["odds"].(float64)
	stake, _ := m["stake"].(float64)
	placedAt, _ := m["placed_at"].(float64)
	return Placement{
		OrderID:  str(m["order_id"]),
		MatchID:  str(m["match_id"]),
		Market:   str(m["market"]),
		Side:     str(m["side"]),
		Odds:     odds,
		Stake:    stake,
		Book:     str(m["book"]),
		Status:   str(m["status"]),
		Note:     str(m["note"]),
		PlacedAt: int64(placedAt),
	}
}

func str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}