package broker

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/redis/go-redis/v9"
)

// Config is the full broker bootstrap contract.
type Config struct {
	RedisURL       string
	Channel        string
	BookmakerWS    string
	BookmakerKey   string
	AuthSecret     string
	MinLatencyMS   int
	SettleChannel  string
	PnlChannel     string
	PnlHash        string
}

// Broker owns both sides of the trade lifecycle.
type Broker struct {
	cfg    Config
	rdb    *redis.Client
	dialer *websocket.Dialer
	once   sync.Once

	mu        sync.Mutex
	positions map[string]Fill
	orderSeq  []string
	session   SessionTotals
}

// New connects to the internal Redis instance without blocking the caller.
func New(cfg Config) (*Broker, error) {
	opts, err := redis.ParseURL(cfg.RedisURL)
	if err != nil {
		return nil, fmt.Errorf("redis parse: %w", err)
	}
	rdb := redis.NewClient(opts)
	if err := rdb.Ping(context.Background()).Err(); err != nil {
		_ = rdb.Close()
		return nil, fmt.Errorf("redis ping: %w", err)
	}
	if cfg.SettleChannel == "" {
		cfg.SettleChannel = "bookmaker:settlements"
	}
	if cfg.PnlChannel == "" {
		cfg.PnlChannel = "execution:pnl"
	}
	if cfg.PnlHash == "" {
		cfg.PnlHash = "pnl:session"
	}
	return &Broker{
		cfg:       cfg,
		rdb:       rdb,
		dialer:    &websocket.Dialer{HandshakeTimeout: 10 * time.Second},
		positions: make(map[string]Fill),
	}, nil
}

// Run subscribes to signed execution commands and dispatches them serially.
func (b *Broker) Run(ctx context.Context) error {
	pubsub := b.rdb.Subscribe(ctx, b.cfg.Channel)
	defer pubsub.Close()

	// Buffered channel so burst traffic doesn't drop commands while a
	// bookmaker round-trip is in flight.
	ch := pubsub.Channel(redis.WithChannelSize(256))
	log.Printf("listening on channel %s", b.cfg.Channel)
	go b.settleLoop(ctx)
	for {
		select {
		case <-ctx.Done():
			return nil
		case msg, ok := <-ch:
			if !ok {
				return fmt.Errorf("subscription closed unexpectedly")
			}
			if err := b.ExecuteEnvelope(ctx, msg.Payload); err != nil {
				log.Printf("rejected command: %v", err)
				continue
			}
			log.Printf("executed command via redis channel")
		}
	}
}

func (b *Broker) dispatch(ctx context.Context, payload *executionPayload) error {
	if payload.Amount <= 0 {
		return fmt.Errorf("stake must be positive")
	}
	conn, _, err := b.dialer.DialContext(ctx, b.cfg.BookmakerWS, nil)
	if err != nil {
		return fmt.Errorf("bookmaker dial: %w", err)
	}
	defer conn.Close()
	if err := b.authHandshake(ctx, conn); err != nil {
		return fmt.Errorf("auth handshake: %w", err)
	}
	fill, err := b.placeOrder(ctx, conn, payload)
	if err != nil {
		return fmt.Errorf("order placement: %w", err)
	}
	b.recordFill(fill)
	return nil
}

// recordFill stores a confirmed order so it can be graded at settlement.
func (b *Broker) recordFill(f *Fill) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, exists := b.positions[f.OrderID]; exists {
		return
	}
	if len(b.orderSeq) >= 500 {
		oldest := b.orderSeq[0]
		b.orderSeq = b.orderSeq[1:]
		delete(b.positions, oldest)
	}
	b.positions[f.OrderID] = *f
	b.orderSeq = append(b.orderSeq, f.OrderID)
}

// settleLoop grades bookmaker settlements from the results bus and publishes
// PnL snapshots for the operator dashboard and the pnl:session hash.
func (b *Broker) settleLoop(ctx context.Context) {
	pubsub := b.rdb.Subscribe(ctx, b.cfg.SettleChannel)
	defer pubsub.Close()
	ch := pubsub.Channel(redis.WithChannelSize(64))
	log.Printf("listening on settlement channel %s", b.cfg.SettleChannel)
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			var s Settlement
			if err := json.Unmarshal([]byte(msg.Payload), &s); err != nil {
				log.Printf("bad settlement frame: %v", err)
				continue
			}
			snapshot := b.grade(&s)
			body, _ := json.Marshal(snapshot)
			if err := b.rdb.Publish(ctx, b.cfg.PnlChannel, body).Err(); err != nil {
				log.Printf("pnl publish failed: %v", err)
			}
			b.persistSession(ctx, snapshot)
			b.forget(snapshot.Settled)
			log.Printf("settled %d order(s) for %s: net %+.2f (won %d / lost %d)",
				len(snapshot.Settled), s.MatchID, snapshot.Session.Net, snapshot.Session.Won, snapshot.Session.Lost)
		}
	}
}

// grade applies settlement to the broker's session totals.
func (b *Broker) grade(s *Settlement) *PnlSnapshot {
	settled := settleOrders(s.Orders, s.FinalScore.Home, s.FinalScore.Away)
	net, won, lost := settlementTotals(settled)

	b.mu.Lock()
	b.session.Net += net
	b.session.Won += won
	b.session.Lost += lost
	snap := &PnlSnapshot{
		Type:       "pnl",
		Cycle:      s.Cycle,
		MatchID:    s.MatchID,
		FinalScore: s.FinalScore,
		Settled:    settled,
		Session:    b.session,
	}
	b.mu.Unlock()
	return snap
}

// forget drops settled order ids so the position store stays bounded.
func (b *Broker) forget(settled []SettledOrder) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, so := range settled {
		if _, ok := b.positions[so.OrderID]; ok {
			delete(b.positions, so.OrderID)
			for i, id := range b.orderSeq {
				if id == so.OrderID {
					b.orderSeq = append(b.orderSeq[:i], b.orderSeq[i+1:]...)
					break
				}
			}
		}
	}
}

// persistSession backs the running totals into a Redis hash so a restart or a
// second operator view can read the accumulated bankroll. Only the current
// settlement's deltas are written; the in-memory session is cumulative.
func (b *Broker) persistSession(ctx context.Context, s *PnlSnapshot) {
	if len(s.Settled) == 0 {
		return
	}
	net, won, lost := settlementTotals(s.Settled)
	pipe := b.rdb.TxPipeline()
	pipe.HIncrByFloat(ctx, b.cfg.PnlHash, "net", net)
	pipe.HIncrBy(ctx, b.cfg.PnlHash, "won", int64(won))
	pipe.HIncrBy(ctx, b.cfg.PnlHash, "lost", int64(lost))
	pipe.HSet(ctx, b.cfg.PnlHash, "updated_cycle", s.Cycle)
	if _, err := pipe.Exec(ctx); err != nil {
		log.Printf("pnl hash update failed: %v", err)
	}
}

func (b *Broker) authHandshake(ctx context.Context, conn *websocket.Conn) error {
	t := time.Now().UnixMilli()
	body, _ := json.Marshal(map[string]any{"ts": t, "key": b.cfg.BookmakerKey})
	sig := sign(body, b.cfg.AuthSecret)
	frame := map[string]any{"auth": base64.StdEncoding.EncodeToString(body), "sig": sig}
	if err := conn.WriteJSON(frame); err != nil {
		return err
	}
	_, resp, err := conn.ReadMessage()
	if err != nil {
		return err
	}
	var ack struct {
		Status string `json:"status"`
		Body   string `json:"body"`
		Sig    string `json:"sig"`
	}
	if err := json.Unmarshal(resp, &ack); err != nil {
		return fmt.Errorf("malformed ack: %w", err)
	}
	if ack.Status != "ok" {
		return fmt.Errorf("auth rejected: %v", ack.Body)
	}
	ackBody, err := base64.StdEncoding.DecodeString(ack.Body)
	if err != nil {
		return fmt.Errorf("ack body decode: %w", err)
	}
	if err := verifyHMAC(ackBody, ack.Sig, b.cfg.AuthSecret); err != nil {
		return fmt.Errorf("ack signature: %w", err)
	}
	return nil
}

func (b *Broker) placeOrder(ctx context.Context, conn *websocket.Conn, p *executionPayload) (*Fill, error) {
	order := map[string]any{
		"type":      "place_order",
		"market":    p.MarketID,
		"side":      p.Side,
		"odds":      p.Odds,
		"stake":     p.Amount,
		"client_ts": time.Now().UnixMilli(),
	}
	body, _ := json.Marshal(order)
	sig := sign(body, b.cfg.AuthSecret)
	envelope := map[string]any{"body": base64.StdEncoding.EncodeToString(body), "sig": sig}
	if err := conn.WriteJSON(envelope); err != nil {
		return nil, err
	}
	// Bound the fill wait by the configured latency budget (default 500ms).
	latency := b.cfg.MinLatencyMS
	if latency <= 0 {
		latency = 500
	}
	deadline := time.Now().Add(time.Duration(latency) * time.Millisecond)
	_ = conn.SetReadDeadline(deadline)
	_, resp, err := conn.ReadMessage()
	if err != nil {
		return nil, fmt.Errorf("response read: %w", err)
	}
	var result struct {
		Status  string  `json:"status"`
		OrderID string  `json:"order_id"`
		Markup  string  `json:"market"`
		Side    string  `json:"side"`
		Odds    float64 `json:"odds"`
		Stake   float64 `json:"stake"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, fmt.Errorf("response unmarshal: %w", err)
	}
	if result.Status != "filled" {
		return nil, fmt.Errorf("order not filled: %v", result)
	}
	if result.OrderID == "" {
		result.OrderID = fmt.Sprintf("L%d", time.Now().UnixNano())
	}
	return &Fill{
		OrderID: result.OrderID,
		Market:  p.MarketID,
		Side:    p.Side,
		Odds:    p.Odds,
		Stake:   p.Amount,
		FillAt:  time.Now(),
	}, nil
}

// Close terminates the Redis connection and any background activity.
func (b *Broker) Close() {
	b.once.Do(func() { _ = b.rdb.Close() })
}

// AuthSecret returns the internal signing secret for caller-side protection.
func (b *Broker) AuthSecret() string {
	return b.cfg.AuthSecret
}

// ExecuteEnvelope verifies a signed envelope and routes it to the bookmaker.
// Shared by both the Redis pub/sub listener and the authenticated HTTP entry.
func (b *Broker) ExecuteEnvelope(ctx context.Context, raw string) error {
	payload, err := decodeEnvelope(raw, b.cfg.AuthSecret)
	if err != nil {
		return fmt.Errorf("verify envelope: %w", err)
	}
	return b.dispatch(ctx, payload)
}

func sign(body []byte, secret string) string {
	h := hmac.New(sha256.New, []byte(secret))
	h.Write(body)
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}

type executionPayload struct {
	MatchID   string  `json:"match_id"`
	MarketID  string  `json:"market_id"`
	Side      string  `json:"side"`
	Odds      float64 `json:"odds"`
	TrueProb  float64 `json:"true_prob"`
	Implied   float64 `json:"implied_prob"`
	Amount    float64 `json:"amount"`
	Timestamp int64   `json:"ts"`
}

func decodeEnvelope(raw string, secret string) (*executionPayload, error) {
	parts := strings.SplitN(raw, ".", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("malformed envelope")
	}
	bodyBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, fmt.Errorf("base64 decode: %w", err)
	}
	if err := verifyHMAC(bodyBytes, parts[1], secret); err != nil {
		return nil, fmt.Errorf("signature: %w", err)
	}
	var p executionPayload
	if err := json.Unmarshal(bodyBytes, &p); err != nil {
		return nil, fmt.Errorf("json unmarshal: %w", err)
	}
	return &p, nil
}

func verifyHMAC(body []byte, sig string, secret string) error {
	h := hmac.New(sha256.New, []byte(secret))
	h.Write(body)
	expected := h.Sum(nil)
	decoded, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil {
		return err
	}
	if !hmac.Equal(expected, decoded) {
		return fmt.Errorf("hmac mismatch")
	}
	return nil
}