package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/casuya-live/execution-engine/internal/api"
	"github.com/casuya-live/execution-engine/internal/broker"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("[execution-engine-go] ")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg := broker.Config{
		RedisURL:      mustEnv("REDIS_URL"),
		Channel:       envOr("EXECUTION_IN_CHANNEL", "execution:commands"),
		BookmakerWS:   mustEnv("BOOKMAKER_WS_URL"),
		BookmakerKey:  mustEnv("BOOKMAKER_API_KEY"),
		AuthSecret:    mustEnv("INTERNAL_AUTH_SECRET"),
		MinLatencyMS:  50,
		SettleChannel: envOr("SETTLE_IN_CHANNEL", "bookmaker:settlements"),
		PnlChannel:    envOr("PNL_OUT_CHANNEL", "execution:pnl"),
		PnlHash:       envOr("PNL_HASH", "pnl:session"),
	}

	b, err := broker.New(cfg)
	if err != nil {
		log.Fatalf("broker init: %v", err)
	}
	defer b.Close()

	// Private-mesh HTTP entry used by analytics-engine-py dispatch().
	// Railway injects PORT; honour it unless LISTEN_ADDR is set explicitly.
	apiSrv := api.New(b, listenAddr(), b.AuthSecret())
	go func() {
		if err := apiSrv.Listen(ctx); err != nil && ctx.Err() == nil {
			log.Printf("http listener exited: %v", err)
		}
	}()

	if err := b.Run(ctx); err != nil && ctx.Err() == nil {
		log.Fatalf("broker exited: %v", err)
	}
	log.Println("shutting down cleanly")
}

func mustEnv(k string) string {
	v := os.Getenv(k)
	if v == "" {
		log.Fatalf("required env %s not set", k)
	}
	return v
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

// listenAddr prefers an explicit LISTEN_ADDR, then Railway's PORT, then :8080.
func listenAddr() string {
	if v := os.Getenv("LISTEN_ADDR"); v != "" {
		return v
	}
	return ":" + envOr("PORT", "8080")
}