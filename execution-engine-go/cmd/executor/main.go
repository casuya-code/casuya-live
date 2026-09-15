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
		BookmakerWS:   envOr("BOOKMAKER_WS_URL", ""),
		BookmakerKey:  envOr("BOOKMAKER_API_KEY", ""),
		AuthSecret:    mustEnv("INTERNAL_AUTH_SECRET"),
		MinLatencyMS:  50,
		SettleChannel: envOr("SETTLE_IN_CHANNEL", "bookmaker:settlements"),
		PnlChannel:    envOr("PNL_OUT_CHANNEL", "execution:pnl"),
		PnlHash:       envOr("PNL_HASH", "pnl:session"),
	}

	// Shared Redis client for the paper broker and the signal API layer.
	rdb, err := broker.ConnectRedis(cfg.RedisURL)
	if err != nil {
		log.Fatalf("redis: %v", err)
	}
	defer rdb.Close()

	// Factory: select execution backend based on environment.
	// PAPER_TRADE=true overrides BROKER_MODE for zero-risk simulation.
	var placer broker.OrderPlacer
	var closer func()

	switch {
	case os.Getenv("PAPER_TRADE") == "true":
		paper := broker.NewPaperBroker(rdb, cfg.PnlHash)
		placer = paper
		go paper.PaperRunLoop(ctx)
		go paper.CommandRunLoop(ctx)
		log.Println("paper trading mode — no real orders")

	default:
		if cfg.BookmakerWS == "" {
			log.Fatal("BOOKMAKER_WS_URL is required for live trading")
		}
		if cfg.BookmakerKey == "" {
			log.Fatal("BOOKMAKER_API_KEY is required for live trading")
		}
		b, err := broker.New(cfg)
		if err != nil {
			log.Fatalf("broker init: %v", err)
		}
		placer = b
		closer = func() { b.Close() }
		go b.Run(ctx)
	}

	if closer != nil {
		defer closer()
	}

	// Private-mesh HTTP entry used by analytics-engine-py dispatch() plus the
	// operator-facing signal endpoints for the dashboard.
	// Railway injects PORT; honour it unless LISTEN_ADDR is set explicitly.
	apiSrv := api.New(placer, listenAddr(), cfg.AuthSecret, rdb)
	go func() {
		if err := apiSrv.Listen(ctx); err != nil && ctx.Err() == nil {
			log.Printf("http listener exited: %v", err)
		}
	}()

	<-ctx.Done()
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