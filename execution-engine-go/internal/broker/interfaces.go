package broker

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// ConnectRedis creates and pings a Redis client. Shared by Broker and PaperBroker.
func ConnectRedis(redisURL string) (*redis.Client, error) {
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, fmt.Errorf("redis parse: %w", err)
	}
	rdb := redis.NewClient(opts)
	if err := rdb.Ping(context.Background()).Err(); err != nil {
		_ = rdb.Close()
		return nil, fmt.Errorf("redis ping: %w", err)
	}
	return rdb, nil
}

// OrderPlacer abstracts the execution backend.
// Implementations: WSBroker (live WebSocket), PaperBroker (simulated), HelabetBroker (future).
type OrderPlacer interface {
	ExecuteEnvelope(ctx context.Context, raw string) error
	PlaceOrder(ctx context.Context, marketID string, side string, odds float64, stake float64) (*Fill, error)
	AuthSecret() string
	Close()
}

// PaperSession tracks cumulative paper trading performance.
type PaperSession struct {
	Net  float64 `json:"net"`
	Won  int     `json:"won"`
	Lost int     `json:"lost"`
}

// PaperBroker simulates order execution with zero financial risk.
// Orders are logged to Redis for offline grading by paperRunLoop.
type PaperBroker struct {
	rdb        *redis.Client
	pnlHash    string
	pnlChannel string
	mu         sync.Mutex
	session    PaperSession
	cycle      int64 // monotonic grading batch counter for PnL snapshots
	slippage   float64 // fraction, e.g. 0.005 = 0.5%
}

// NewPaperBroker creates a paper trading broker with conservative slippage.
func NewPaperBroker(rdb *redis.Client, pnlHash string) *PaperBroker {
	if pnlHash == "" {
		pnlHash = "pnl:session"
	}
	pnlChannel := os.Getenv("PNL_OUT_CHANNEL")
	if pnlChannel == "" {
		pnlChannel = "execution:pnl"
	}
	return &PaperBroker{
		rdb:        rdb,
		pnlHash:    pnlHash,
		pnlChannel: pnlChannel,
		slippage:   0.005, // 0.5% live execution slippage
	}
}

// PlaceOrder simulates a fill with slippage and logs to Redis.
func (p *PaperBroker) PlaceOrder(ctx context.Context, marketID string, side string, odds float64, stake float64) (*Fill, error) {
	if stake <= 0 {
		return nil, fmt.Errorf("stake must be positive")
	}

	// Simulate live execution slippage: odds degrade slightly
	fillPrice := odds - (odds * p.slippage)

	fill := &Fill{
		OrderID: fmt.Sprintf("paper_%d", time.Now().UnixNano()),
		Market:  marketID,
		Side:    side,
		Odds:    fillPrice,
		Stake:   stake,
		FillAt:  time.Now(),
	}

	payload, err := json.Marshal(fill)
	if err != nil {
		return nil, fmt.Errorf("marshal paper fill: %w", err)
	}
	if p.rdb == nil {
		return nil, fmt.Errorf("paper trade log: redis client unavailable")
	}
	if err := p.rdb.HSet(ctx, "execution:paper_orders", fill.OrderID, payload).Err(); err != nil {
		return nil, fmt.Errorf("paper trade log: %w", err)
	}

	log.Printf("[paper] order %s: %s %s @%.2f (raw %.2f) stake %.2f",
		fill.OrderID, marketID, side, fillPrice, odds, stake)
	return fill, nil
}

// PaperRunLoop subscribes to matches:live and grades paper orders on FULLTIME.
// It uses the final score from the match frame to grade pending paper orders.
func (p *PaperBroker) PaperRunLoop(ctx context.Context) {
	channel := os.Getenv("ANALYTICS_IN_CHANNEL")
	if channel == "" {
		channel = "matches:live"
	}
	pubsub := p.rdb.Subscribe(ctx, channel)
	defer pubsub.Close()
	ch := pubsub.Channel(redis.WithChannelSize(64))
	log.Printf("[paper] listening on %s for settlement grading", channel)

	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			var frame struct {
				Clock   string `json:"clock"`
				MatchID string `json:"match_id"`
				Score   *struct {
					Home int `json:"home"`
					Away int `json:"away"`
				} `json:"score"`
			}
			if err := json.Unmarshal([]byte(msg.Payload), &frame); err != nil {
				continue
			}
			if frame.Clock != "FULLTIME" || frame.Score == nil {
				continue
			}
			// Read paper orders from the hash, grading only those that belong
			// to the finished match. Orders for other in-play matches must
			// stay pending until their own FULLTIME frame arrives.
			entries, err := p.rdb.HGetAll(ctx, "execution:paper_orders").Result()
			if err != nil || len(entries) == 0 {
				continue
			}
			var orders []SettlementOrder
			var ids []string
			for id, raw := range entries {
				var f Fill
				if err := json.Unmarshal([]byte(raw), &f); err != nil {
					continue
				}
				if f.MatchID != "" && f.MatchID != frame.MatchID {
					continue
				}
				orders = append(orders, SettlementOrder{
					OrderID: f.OrderID,
					Market:  f.Market,
					Side:    f.Side,
					Odds:    f.Odds,
					Stake:   f.Stake,
				})
				ids = append(ids, id)
			}
			if len(orders) == 0 {
				continue
			}
			settled := settleOrders(orders, float64(frame.Score.Home), float64(frame.Score.Away))
			p.mu.Lock()
			p.cycle++
			batch := p.cycle
			p.mu.Unlock()
			p.persistSession(ctx, settled, batch)
			p.recordSettlements(ctx, frame.MatchID, float64(frame.Score.Home), float64(frame.Score.Away), settled)
			p.publishPaperSnapshot(ctx, frame.MatchID, frame.Score.Home, frame.Score.Away, settled, batch)
			// Clear only the graded orders
			p.rdb.HDel(ctx, "execution:paper_orders", ids...)
			net, _, _ := settlementTotals(settled)
			log.Printf("[paper] settled %d order(s) for %s %d-%d: net %+.2f",
				len(settled), frame.MatchID, frame.Score.Home, frame.Score.Away, net)
		}
	}
}

// persistSession backs paper trade deltas into the same PnL hash as live trades.
func (p *PaperBroker) persistSession(ctx context.Context, settled []SettledOrder, cycle int64) {
	if len(settled) == 0 {
		return
	}
	net, won, lost := settlementTotals(settled)
	p.mu.Lock()
	p.session.Net += net
	p.session.Won += won
	p.session.Lost += lost
	p.mu.Unlock()

	pipe := p.rdb.TxPipeline()
	pipe.HIncrByFloat(ctx, p.pnlHash, "net", net)
	pipe.HIncrBy(ctx, p.pnlHash, "won", int64(won))
	pipe.HIncrBy(ctx, p.pnlHash, "lost", int64(lost))
	pipe.HSet(ctx, p.pnlHash, "source", "paper")
	pipe.HSet(ctx, p.pnlHash, "updated_cycle", cycle)
	if _, err := pipe.Exec(ctx); err != nil {
		log.Printf("[paper] pnl hash update failed: %v", err)
	}
}

// Close is a no-op for PaperBroker (no WebSocket to close).
func (p *PaperBroker) Close() {}

// paperSettlementsStream is the append-only ledger of graded paper trades,
// consumed by the dashboard's settled-order table.
const paperSettlementsStream = "execution:paper_settlements"

// recordSettlements appends graded paper orders to the settlement ledger
// stream so the dashboard can render the historical performance table.
func (p *PaperBroker) recordSettlements(ctx context.Context, matchID string, home, away float64, settled []SettledOrder) {
	if len(settled) == 0 {
		return
	}
	pipe := p.rdb.Pipeline()
	now := time.Now().UnixMilli()
	for _, so := range settled {
		pipe.XAdd(ctx, &redis.XAddArgs{
			Stream: paperSettlementsStream,
			MaxLen: 1000,
			Approx: true,
			Values: map[string]any{
				"order_id":   so.OrderID,
				"match_id":   matchID,
				"market":     so.Market,
				"side":       so.Side,
				"odds":       so.Odds,
				"stake":      so.Stake,
				"result":     so.Result,
				"pnl":        so.Pnl,
				"score_home": home,
				"score_away": away,
				"settled_at": now,
			},
		})
	}
	if _, err := pipe.Exec(ctx); err != nil {
		log.Printf("[paper] settlement ledger write failed: %v", err)
	}
}

// publishPaperSnapshot broadcasts the graded batch on the PnL channel so the
// operator console and relay statistics stay live in paper mode, identical to
// the live Broker's PnlSnapshot publications.
func (p *PaperBroker) publishPaperSnapshot(ctx context.Context, matchID string, home, away int, settled []SettledOrder, cycle int64) {
	if len(settled) == 0 {
		return
	}
	p.mu.Lock()
	snap := PnlSnapshot{
		Type:    "pnl",
		Cycle:   cycle,
		MatchID: matchID,
		FinalScore: Score{
			Home: float64(home),
			Away: float64(away),
		},
		Settled: settled,
		Session: SessionTotals{
			Net:  p.session.Net,
			Won:  p.session.Won,
			Lost: p.session.Lost,
		},
	}
	p.mu.Unlock()
	body, err := json.Marshal(snap)
	if err != nil {
		return
	}
	if err := p.rdb.Publish(ctx, p.pnlChannel, body).Err(); err != nil {
		log.Printf("[paper] pnl publish failed: %v", err)
	}
}

// ExecuteEnvelope satisfies the envelopeExecutor interface used by the API handler.
// It decodes the signed envelope from analytics-engine-py and routes to PlaceOrder,
// then enriches the stored fill with match context and broadcasts a live signal.
func (p *PaperBroker) ExecuteEnvelope(ctx context.Context, raw string) error {
	payload, err := decodeEnvelope(raw, "")
	if err != nil {
		return fmt.Errorf("verify envelope: %w", err)
	}
	fill, err := p.PlaceOrder(ctx, payload.MarketID, payload.Side, payload.Odds, payload.Amount)
	if err != nil {
		return err
	}
	if fill.MatchID == "" {
		fill.MatchID = payload.MatchID
		if body, err := json.Marshal(fill); err == nil {
			if err := p.rdb.HSet(ctx, "execution:paper_orders", fill.OrderID, body).Err(); err != nil {
				log.Printf("[paper] signal record update failed: %v", err)
			}
		}
	}
	publishSignal(ctx, p.rdb, fill, payload.Odds, payload.TrueProb, payload.Implied)
	return nil
}

// AuthSecret returns empty for PaperBroker (no HMAC signing needed).
func (p *PaperBroker) AuthSecret() string { return "" }

// CommandRunLoop subscribes to execution:commands and routes to PlaceOrder.
// This is the paper mode equivalent of Broker.Run().
func (p *PaperBroker) CommandRunLoop(ctx context.Context) {
	channel := os.Getenv("EXECUTION_IN_CHANNEL")
	if channel == "" {
		channel = "execution:commands"
	}
	pubsub := p.rdb.Subscribe(ctx, channel)
	defer pubsub.Close()
	ch := pubsub.Channel(redis.WithChannelSize(256))
	log.Printf("[paper] listening on channel %s", channel)

	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			if err := p.ExecuteEnvelope(ctx, msg.Payload); err != nil {
				log.Printf("[paper] rejected command: %v", err)
				continue
			}
			log.Printf("[paper] executed command via redis channel")
		}
	}
}
