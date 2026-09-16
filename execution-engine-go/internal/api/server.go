package api

import (
	"context"
	"encoding/json"
	"log"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/casuya-live/execution-engine/internal/broker"
	"github.com/gorilla/websocket"
	"github.com/redis/go-redis/v9"
)

// Handler wraps the broker for the private-mesh HTTP command entrypoint and
// exposes the operator-facing signal API for the dashboard.
type Handler struct {
	broker      OrderPlacer
	server      *http.Server
	accessToken string
	rdb         *redis.Client
}

// OrderPlacer is the interface the API handler needs from the broker.
type OrderPlacer interface {
	ExecuteEnvelope(ctx context.Context, raw string) error
	AuthSecret() string
}

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin:     func(*http.Request) bool { return true },
}

// New builds an authenticated command listener bound to the given address.
// The token secret mirrors INTERNAL_AUTH_SECRET for defense-in-depth; the
// envelope HMAC remains the authoritative integrity check. rdb powers the
// signal endpoints and may be nil when the API layer is used in isolation.
func New(broker OrderPlacer, addr string, token string, rdb *redis.Client) *Handler {
	h := &Handler{broker: broker, accessToken: token, rdb: rdb}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", h.healthz)
	mux.HandleFunc("/execute", h.execute)
	mux.HandleFunc("/api/v1/stream/signals", h.streamSignals)
	mux.HandleFunc("/api/v1/signals/active", h.activeSignals)
	mux.HandleFunc("/api/v1/analytics/pnl", h.analyticsPnL)
	mux.HandleFunc("/api/v1/analytics/ledger", h.analyticsLedger)
	mux.HandleFunc("/api/v1/admin/analytics/reset", h.adminResetAnalytics)
	mux.HandleFunc("/api/v1/operator/place", h.operatorPlace)
	mux.HandleFunc("/api/v1/operator/placements", h.operatorPlacements)
	h.server = &http.Server{
		Addr:              addr,
		Handler:           withCORS(mux),
		ReadHeaderTimeout: 3 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	return h
}

// withCORS allows browser dashboards (Vercel) to call the engine's public
// signal endpoints directly. The envelope HMAC remains the authoritative
// integrity check for /execute.
func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Internal-Token")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Listen starts the HTTP server and blocks until ctx is cancelled.
func (h *Handler) Listen(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() { errCh <- h.server.ListenAndServe() }()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = h.server.Shutdown(shutdownCtx)
		return nil
	case err := <-errCh:
		return err
	}
}

func (h *Handler) healthz(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func (h *Handler) execute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// Skip token check when accessToken is empty (paper trading mode).
	if h.accessToken != "" {
		if secret := r.Header.Get("X-Internal-Token"); secret == "" || h.accessToken != secret {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
	}
	var envelope struct {
		Envelope string `json:"envelope"`
	}
	defer r.Body.Close()
	if err := json.NewDecoder(r.Body).Decode(&envelope); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if err := h.broker.ExecuteEnvelope(r.Context(), envelope.Envelope); err != nil {
		log.Printf("http execute rejected: %v", err)
		http.Error(w, "execution rejected", http.StatusUnprocessableEntity)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"status":"filled"}`))
}

// streamSignals is a WebSocket endpoint that relays each live signal published
// on the market:signals:live channel straight to the connected dashboard.
func (h *Handler) streamSignals(w http.ResponseWriter, r *http.Request) {
	if h.rdb == nil {
		http.Error(w, "redis unavailable", http.StatusServiceUnavailable)
		return
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("signal ws upgrade failed: %v", err)
		return
	}
	defer conn.Close()

	pubsub := h.rdb.Subscribe(context.Background(), broker.SignalChannel())
	defer pubsub.Close()
	ch := pubsub.Channel(redis.WithChannelSize(64))

	// Reader goroutine closes done on disconnect so the writer loop exits.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	write := func(msgType int, data []byte) bool {
		_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		if err := conn.WriteMessage(msgType, data); err != nil {
			return false
		}
		return true
	}

	for {
		select {
		case <-done:
			return
		case <-r.Context().Done():
			return
		case <-ticker.C:
			if !write(websocket.PingMessage, nil) {
				return
			}
		case msg, ok := <-ch:
			if !ok {
				return
			}
			if !write(websocket.TextMessage, []byte(msg.Payload)) {
				return
			}
		}
	}
}

// activeSignals lists the currently unsettled paper orders as signal views.
func (h *Handler) activeSignals(w http.ResponseWriter, r *http.Request) {
	if h.rdb == nil {
		http.Error(w, "redis unavailable", http.StatusServiceUnavailable)
		return
	}
	entries, err := h.rdb.HGetAll(r.Context(), "execution:paper_orders").Result()
	if err != nil {
		http.Error(w, "redis read failed", http.StatusInternalServerError)
		return
	}
	out := make([]map[string]any, 0, len(entries))
	for _, raw := range entries {
		var f broker.Fill
		if err := json.Unmarshal([]byte(raw), &f); err != nil {
			continue
		}
		out = append(out, broker.BuildSignal(r.Context(), h.rdb, &f, 0, 0, 0))
	}
	writeJSON(w, map[string]any{
		"type":    "signals",
		"count":   len(out),
		"signals": out,
	})
}

// analyticsPnL returns the cumulative paper-trading performance stats.
func (h *Handler) analyticsPnL(w http.ResponseWriter, r *http.Request) {
	if h.rdb == nil {
		http.Error(w, "redis unavailable", http.StatusServiceUnavailable)
		return
	}
	vals, err := h.rdb.HGetAll(r.Context(), broker.PnlHashName()).Result()
	if err != nil {
		http.Error(w, "redis read failed", http.StatusInternalServerError)
		return
	}
	netVal, _ := strconv.ParseFloat(vals["net"], 64)
	wonVal, _ := strconv.Atoi(vals["won"])
	lostVal, _ := strconv.Atoi(vals["lost"])
	voidVal, _ := strconv.Atoi(vals["voids"])
	total := wonVal + lostVal + voidVal
	winRate := 0.0
	if wonVal+lostVal > 0 {
		winRate = math.Round(float64(wonVal)*10000/float64(wonVal+lostVal)) / 100
	}
	voidRate := 0.0
	if total > 0 {
		voidRate = math.Round(float64(voidVal)*10000/float64(total)) / 100
	}
	writeJSON(w, map[string]any{
		"type":          "pnl",
		"net":           netVal,
		"won":           wonVal,
		"lost":          lostVal,
		"voids":         voidVal,
		"total_bets":    total,
		"win_rate":      winRate,
		"void_rate":     voidRate,
		"source":        vals["source"],
		"updated_cycle": vals["updated_cycle"],
		"updated_at":    time.Now().UnixMilli(),
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// analyticsLedger returns the most recent graded paper settlements from the
// execution:paper_settlements stream, newest first, capped by ?limit=.
func (h *Handler) analyticsLedger(w http.ResponseWriter, r *http.Request) {
	if h.rdb == nil {
		http.Error(w, "redis unavailable", http.StatusServiceUnavailable)
		return
	}
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}
	msgs, err := h.rdb.XRevRangeN(r.Context(), "execution:paper_settlements", "+", "-", int64(limit)).Result()
	if err != nil {
		http.Error(w, "redis read failed", http.StatusInternalServerError)
		return
	}
	entries := make([]map[string]any, 0, len(msgs))
	for _, m := range msgs {
		e := make(map[string]any, len(m.Values)+1)
		for k, v := range m.Values {
			switch k {
			case "odds", "pnl", "stake", "score_home", "score_away", "settled_at", "verified":
				e[k] = numericValue(v)
			default:
				e[k] = v
			}
		}
		e["id"] = m.ID
		entries = append(entries, e)
	}
	writeJSON(w, map[string]any{"type": "ledger", "count": len(entries), "entries": entries})
}

// numericValue coerces a Redis stream field back to a number when possible.
func numericValue(v any) any {
	s, ok := v.(string)
	if !ok {
		return v
	}
	if n, err := strconv.ParseFloat(s, 64); err == nil {
		return n
	}
	return s
}

// adminResetAnalytics purges the accumulated paper PnL counters and the ledger
// used by the operator desk. Guarded by the shared internal auth token.
// Request body: {"keep_ledger": true} to leave the settlement stream intact.
func (h *Handler) adminResetAnalytics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if secret := r.Header.Get("X-Internal-Token"); secret == "" || h.accessToken != secret {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.rdb == nil {
		http.Error(w, "redis unavailable", http.StatusServiceUnavailable)
		return
	}
	var body struct {
		KeepLedger bool `json:"keep_ledger"`
	}
	if r.Body != nil {
		defer r.Body.Close()
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	ctx := r.Context()
	if err := h.rdb.Del(ctx, broker.PnlHashName()).Err(); err != nil {
		http.Error(w, "pnl reset failed", http.StatusInternalServerError)
		return
	}
	if !body.KeepLedger {
		if err := h.rdb.Del(ctx, broker.PaperSettlementsStream()).Err(); err != nil {
			http.Error(w, "ledger reset failed", http.StatusInternalServerError)
			return
		}
	}
	writeJSON(w, map[string]any{"status": "reset", "keep_ledger": body.KeepLedger})
}

// operatorPlace records a manual bookmaker placement the operator made against
// a signal (operator-assisted live trading). Paper grading stays authoritative
// for PnL; this only drives the operator desk UI.
func (h *Handler) operatorPlace(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.rdb == nil {
		http.Error(w, "redis unavailable", http.StatusServiceUnavailable)
		return
	}
	defer r.Body.Close()
	var p broker.Placement
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if p.OrderID == "" {
		http.Error(w, "order_id required", http.StatusBadRequest)
		return
	}
	switch p.Status {
	case "placed", "void", "failed":
	default:
		http.Error(w, "status must be placed, void or failed", http.StatusBadRequest)
		return
	}
	if p.Book == "" {
		p.Book = "helabet"
	}
	if p.PlacedAt == 0 {
		p.PlacedAt = time.Now().UnixMilli()
	}
	if err := broker.RecordPlacement(r.Context(), h.rdb, p); err != nil {
		http.Error(w, "record failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"status": "recorded", "order_id": p.OrderID})
}

// operatorPlacements returns recent operator placements newest-first, capped
// by ?limit=.
func (h *Handler) operatorPlacements(w http.ResponseWriter, r *http.Request) {
	if h.rdb == nil {
		http.Error(w, "redis unavailable", http.StatusServiceUnavailable)
		return
	}
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}
	msgs, err := h.rdb.XRevRangeN(r.Context(), broker.OperatorStreamDefault, "+", "-", int64(limit)).Result()
	if err != nil {
		http.Error(w, "redis read failed", http.StatusInternalServerError)
		return
	}
	entries := make([]map[string]any, 0, len(msgs))
	for _, m := range msgs {
		e := make(map[string]any, len(m.Values)+1)
		for k, v := range m.Values {
			switch k {
			case "odds", "stake", "placed_at":
				e[k] = numericValue(v)
			default:
				e[k] = v
			}
		}
		e["id"] = m.ID
		entries = append(entries, e)
	}
	writeJSON(w, map[string]any{"type": "placements", "count": len(entries), "entries": entries})
}