package broker

import (
	"context"
	"encoding/json"
	"log"
	"os"

	"github.com/redis/go-redis/v9"
)

// SignalChannelDefault is the Redis pub/sub channel broadcasting live signals
// to operator dashboards.
const SignalChannelDefault = "market:signals:live"

// PnlHashDefault is the Redis hash accumulating session PnL totals.
const PnlHashDefault = "pnl:session"

// SignalChannel returns the configured live-signal channel (SIGNAL_CHANNEL env).
func SignalChannel() string {
	if v := os.Getenv("SIGNAL_CHANNEL"); v != "" {
		return v
	}
	return SignalChannelDefault
}

// PnlHashName returns the configured PnL hash key (PNL_HASH env).
func PnlHashName() string {
	if v := os.Getenv("PNL_HASH"); v != "" {
		return v
	}
	return PnlHashDefault
}

// BuildSignal renders the operator-facing signal view for a fill, enriching it
// with match metadata (teams, league, clock) from the match hash when available.
func BuildSignal(ctx context.Context, rdb *redis.Client, fill *Fill, rawOdds, trueProb, impliedProb float64) map[string]any {
	sig := map[string]any{
		"type":         "signal",
		"order_id":     fill.OrderID,
		"match_id":     fill.MatchID,
		"market":       fill.Market,
		"side":         fill.Side,
		"market_label": marketLabel(fill.Market, fill.Side),
		"odds":         fill.Odds,
		"stake":        fill.Stake,
		"timestamp":    fill.FillAt.UnixMilli(),
	}
	if rawOdds > 0 {
		sig["raw_odds"] = rawOdds
	}
	if trueProb > 0 {
		sig["true_prob"] = trueProb
	}
	if impliedProb > 0 {
		sig["implied_prob"] = impliedProb
	}
	if fill.MatchID != "" && rdb != nil {
		if meta, err := rdb.HGetAll(ctx, "match:"+fill.MatchID).Result(); err == nil {
			for _, k := range []string{"home_team", "away_team", "league", "clock"} {
				if v := meta[k]; v != "" {
					sig[k] = v
				}
			}
		}
	}
	return sig
}

func marketLabel(market, side string) string {
	return market + " " + side
}

// publishSignal broadcasts a fill's signal view to the live signal channel.
// Best-effort: a failed publish never fails the order.
func publishSignal(ctx context.Context, rdb *redis.Client, fill *Fill, rawOdds, trueProb, impliedProb float64) {
	if rdb == nil || fill == nil {
		return
	}
	body, err := json.Marshal(BuildSignal(ctx, rdb, fill, rawOdds, trueProb, impliedProb))
	if err != nil {
		log.Printf("signal marshal failed: %v", err)
		return
	}
	if err := rdb.Publish(ctx, SignalChannel(), body).Err(); err != nil {
		log.Printf("signal publish failed: %v", err)
	}
}