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
	RedisURL     string
	Channel      string
	BookmakerWS  string
	BookmakerKey string
	AuthSecret   string
	MinLatencyMS int
}

// Broker owns both sides of the trade lifecycle.
type Broker struct {
	cfg    Config
	rdb    *redis.Client
	dialer *websocket.Dialer
	once   sync.Once
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
	return &Broker{
		cfg:    cfg,
		rdb:    rdb,
		dialer: &websocket.Dialer{HandshakeTimeout: 10 * time.Second},
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
	if err := b.placeOrder(ctx, conn, payload); err != nil {
		return fmt.Errorf("order placement: %w", err)
	}
	return nil
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

func (b *Broker) placeOrder(ctx context.Context, conn *websocket.Conn, p *executionPayload) error {
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
		return err
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
		return fmt.Errorf("response read: %w", err)
	}
	var result map[string]any
	if err := json.Unmarshal(resp, &result); err != nil {
		return fmt.Errorf("response unmarshal: %w", err)
	}
	if result["status"] != "filled" {
		return fmt.Errorf("order not filled: %v", result)
	}
	return nil
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