package stream

import (
	"context"
	"encoding/json"
	"log"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

// Config holds transport-level settings for the vendor stream connection.
type Config struct {
	ProviderURL        string
	MarketsScope       string
	ReconnectBaseDelay time.Duration
	ReconnectMaxDelay  time.Duration
}

// Sink receives a validated match snapshot once it survives inbound validation.
type Sink func(context.Context, Match) error

// Handler owns the vendor WebSocket connection lifecycle and feeds the cache layer.
type Handler struct {
	cfg   Config
	sink  Sink
	dial  *websocket.Dialer
	scope MarketScope
}

// NewHandler builds a handler whose sink is invoked for every validated match frame.
func NewHandler(cfg Config, sink Sink) *Handler {
	return &Handler{
		cfg:   cfg,
		sink:  sink,
		dial:  &websocket.Dialer{HandshakeTimeout: 10 * time.Second},
		scope: parseScope(cfg.MarketsScope),
	}
}

// parseScope converts a comma-separated market list into a lookup set.
func parseScope(raw string) MarketScope {
	scope := MarketScope{}
	for _, id := range strings.Split(raw, ",") {
		if id = strings.TrimSpace(id); id != "" {
			scope[id] = true
		}
	}
	if len(scope) == 0 {
		scope = DefaultScope()
	}
	return scope
}

// Run blocks until ctx is cancelled or a non-retryable fatal error occurs.
func (h *Handler) Run(ctx context.Context) error {
	delay := h.cfg.ReconnectBaseDelay
	for {
		err := h.runSingleConnection(ctx)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return nil
		}
		log.Printf("provider connection failed: %v; retrying in %s", err, delay)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
		if delay < h.cfg.ReconnectMaxDelay {
			delay *= 2
			if delay > h.cfg.ReconnectMaxDelay {
				delay = h.cfg.ReconnectMaxDelay
			}
		}
	}
}

func (h *Handler) runSingleConnection(ctx context.Context) error {
	conn, _, err := h.dial.DialContext(ctx, h.cfg.ProviderURL, nil)
	if err != nil {
		return err
	}
	defer conn.Close()

	// Child context so the ping goroutine exits when THIS connection ends,
	// not when the whole process shuts down (otherwise every reconnect
	// leaks a goroutine ticking against a closed socket).
	connCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go h.pingLoop(connCtx, conn)
	for {
		_, payload, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		match, ok := h.parseFrame(payload)
		if !ok {
			continue // malformed or out-of-scope frame; discarded at the boundary
		}
		if err := h.sink(ctx, match); err != nil {
			log.Printf("cache write failed for match %s: %v", match.MatchID, err)
		}
	}
}

func (h *Handler) pingLoop(ctx context.Context, conn *websocket.Conn) {
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second))
		}
	}
}

// parseFrame validates an inbound JSON packet against the scoped market contract.
func (h *Handler) parseFrame(payload []byte) (Match, bool) {
	var raw matchFrame
	if err := json.Unmarshal(payload, &raw); err != nil {
		return Match{}, false
	}
	match := raw.toMatch()
	match.FilterScope(h.scope)
	if err := match.Validate(); err != nil {
		return Match{}, false
	}
	return match, true
}