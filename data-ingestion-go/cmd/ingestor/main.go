package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/casuya-live/data-ingestion/internal/apifootball"
	"github.com/casuya-live/data-ingestion/internal/betpawa"
	"github.com/casuya-live/data-ingestion/internal/cache"
	"github.com/casuya-live/data-ingestion/internal/helabet"
	"github.com/casuya-live/data-ingestion/internal/stream"
)

const (
	envProviderURL  = "PROVIDER_WS_URL"
	envProviderMode = "PROVIDER_MODE"
	envRedisURL     = "REDIS_URL"
	envMarketsScope = "MARKETS_SCOPE"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("[data-ingestion-go] ")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	redis, err := cache.New(ctx, mustEnv(envRedisURL))
	if err != nil {
		log.Fatalf("redis: %v", err)
	}
	defer redis.Close()

	mode := envOr(envProviderMode, "mock")

	switch mode {
	case "apifootball":
		key := os.Getenv("APIFOOTBALL_KEY")
		if key == "" {
			log.Fatalf("APIFOOTBALL_KEY is required when PROVIDER_MODE=apifootball")
		}
		interval := 30 * time.Second
		if v := os.Getenv("APIFOOTBALL_POLL_SECONDS"); v != "" {
			if n, err := time.ParseDuration(v + "s"); err == nil && n > 0 {
				interval = n
			}
		}
		poller, err := apifootball.New(ctx, key, interval)
		if err != nil {
			log.Fatalf("apifootball poller init: %v", err)
		}
		log.Printf("starting API-Football poller (interval %s)", interval)
		if err := poller.Run(ctx, redis.PublishMatch); err != nil && ctx.Err() == nil {
			log.Fatalf("apifootball poller exited: %v", err)
		}
	case "helabet":
		// Direct public live-feed ingestion from Helabet's own EveryMatrix
		// gateway (the book's prices = the true prices the operator bets).
		interval := 5 * time.Second
		if v := os.Getenv("HELABET_POLL_SECONDS"); v != "" {
			if n, err := time.ParseDuration(v + "s"); err == nil && n > 0 {
				interval = n
			}
		}
		poller := helabet.New(helabet.Config{
			BaseURL:  envOr("HELABET_BASE_URL", "https://helabet.co.tz"),
			Interval: interval,
		})
		log.Printf("starting Helabet live poller (interval %s)", interval)
		if err := poller.Run(ctx, redis.PublishMatch); err != nil && ctx.Err() == nil {
			log.Fatalf("helabet poller exited: %v", err)
		}
	case "betpawa":
		// pawablox / Zola sportsbook JSON gateway — the book's own live odds.
		interval := 5 * time.Second
		if v := os.Getenv("BETPAWA_POLL_SECONDS"); v != "" {
			if n, err := time.ParseDuration(v + "s"); err == nil && n > 0 {
				interval = n
			}
		}
		poller := betpawa.New(betpawa.Config{
			BaseURL:  envOr("BETPAWA_BASE_URL", "https://www.betpawa.co.tz"),
			Brand:    envOr("BETPAWA_BRAND", "betpawa-tanzania"),
			Interval: interval,
		})
		log.Printf("starting betPawa live poller (interval %s)", interval)
		if err := poller.Run(ctx, redis.PublishMatch); err != nil && ctx.Err() == nil {
			log.Fatalf("betpawa poller exited: %v", err)
		}
	default:
		if os.Getenv(envProviderURL) == "" {
			log.Fatalf("required environment variable %s is not set", envProviderURL)
		}
		cfg := stream.Config{
			ProviderURL:        mustEnv(envProviderURL),
			MarketsScope:       envOr(envMarketsScope, "1H_1X2,2H_1X2,CS_1H,CS_2H"),
			ReconnectBaseDelay: 500 * time.Millisecond,
			ReconnectMaxDelay:  30 * time.Second,
		}
		handler := stream.NewHandler(cfg, redis.PublishMatch)
		if err := handler.Run(ctx); err != nil && ctx.Err() == nil {
			log.Fatalf("stream handler exited: %v", err)
		}
	}

	log.Println("shutting down cleanly")
}

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		log.Fatalf("required environment variable %s is not set", key)
	}
	return v
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}