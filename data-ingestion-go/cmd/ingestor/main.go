package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/casuya-live/data-ingestion/internal/cache"
	"github.com/casuya-live/data-ingestion/internal/stream"
)

const (
	envProviderURL  = "PROVIDER_WS_URL"
	envRedisURL     = "REDIS_URL"
	envMarketsScope = "MARKETS_SCOPE"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("[data-ingestion-go] ")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg := stream.Config{
		ProviderURL:        mustEnv(envProviderURL),
		MarketsScope:       envOr(envMarketsScope, "1H_1X2,2H_1X2,CS_1H,CS_2H"),
		ReconnectBaseDelay: 500 * time.Millisecond,
		ReconnectMaxDelay:  30 * time.Second,
	}

	redis, err := cache.New(ctx, mustEnv(envRedisURL))
	if err != nil {
		log.Fatalf("redis: %v", err)
	}
	defer redis.Close()

	handler := stream.NewHandler(cfg, redis.PublishMatch)
	if err := handler.Run(ctx); err != nil && ctx.Err() == nil {
		log.Fatalf("stream handler exited: %v", err)
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