package api

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"time"
)

// Handler wraps the broker for the private-mesh HTTP command entrypoint.
type Handler struct {
	broker      envelopeExecutor
	server      *http.Server
	accessToken string
}

type envelopeExecutor interface {
	ExecuteEnvelope(ctx context.Context, raw string) error
}

// New builds an authenticated command listener bound to the given address.
// The token secret mirrors INTERNAL_AUTH_SECRET for defense-in-depth; the
// envelope HMAC remains the authoritative integrity check.
func New(broker envelopeExecutor, addr string, token string) *Handler {
	h := &Handler{broker: broker, accessToken: token}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", h.healthz)
	mux.HandleFunc("/execute", h.execute)
	h.server = &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 3 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	return h
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
	if secret := r.Header.Get("X-Internal-Token"); secret == "" || h.accessToken != secret {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
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