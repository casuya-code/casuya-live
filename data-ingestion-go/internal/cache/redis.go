package cache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/casuya-live/data-ingestion/internal/stream"
	"github.com/redis/go-redis/v9"
)

const (
	hashKeyPrefix = "match:"
	// streamKey is the Redis Stream holding durable match history.
	streamKey = "matches:live"
	// channelKey is the Pub/Sub channel the analytics engine subscribes to.
	// NOTE: Pub/Sub channels live in a separate namespace from keys, so this
	// may share a name with the Stream — but the XADD above does NOT wake
	// SUBSCRIBErs. PublishMatch must explicitly Publish each frame.
	channelKey = "matches:live"
)

// ErrClosed is returned when writing through a closed cache client.
var ErrClosed = errors.New("redis cache is closed")

// Redis is a thin, low-latency persistence layer mapping validated matches
// onto Redis Hashes and a Stream, and publishing each frame so pub/sub
// consumers wake on new frames.
type Redis struct {
	client *redis.Client
	done   chan struct{}
	once   sync.Once
}

// New connects to the internal Redis instance and verifies liveness.
func New(ctx context.Context, url string) (*Redis, error) {
	opts, err := redis.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("parse redis url: %w", err)
	}
	client := redis.NewClient(opts)
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("ping redis: %w", err)
	}
	return &Redis{client: client, done: make(chan struct{})}, nil
}

// PublishMatch writes a validated match to a hash and appends it to the live
// stream, guaranteeing sub-millisecond availability for analytics consumers.
func (r *Redis) PublishMatch(ctx context.Context, match stream.Match) error {
	select {
	case <-r.done:
		return ErrClosed
	default:
	}
	key := hashKeyPrefix + match.MatchID
	fields := map[string]any{
		"sport":         match.Sport,
		"league":        match.League,
		"home_team":     match.HomeTeam,
		"away_team":     match.AwayTeam,
		"kickoff":       match.Kickoff.Unix(),
		"clock":         match.Clock,
		"score_home":    match.Score.Home,
		"score_away":    match.Score.Away,
		"received_at":   match.ReceivedAt.UnixMilli(),
		"markets_total": len(match.Markets),
	}
	for id, market := range match.Markets {
		fields["odds_"+id+"_home"] = strconv.FormatFloat(market.Odds.Home, 'f', 2, 64)
		fields["odds_"+id+"_draw"] = strconv.FormatFloat(market.Odds.Draw, 'f', 2, 64)
		fields["odds_"+id+"_away"] = strconv.FormatFloat(market.Odds.Away, 'f', 2, 64)
		// Correct-Score markets have no 1X2 semantics; only label 1X2 legs.
		// The map key is authoritative (scope was filtered on it).
		if id == stream.Market1H1X2 || id == stream.Market2H1X2 {
			fields["winner_"+id] = winnerOf(market)
		}
	}

	frame, err := json.Marshal(match)
	if err != nil {
		return fmt.Errorf("marshal match: %w", err)
	}

	pipe := r.client.TxPipeline()
	pipe.HSet(ctx, key, fields)
	pipe.Expire(ctx, key, 24*time.Hour)
	pipe.XAdd(ctx, &redis.XAddArgs{
		Stream: streamKey,
		Values: map[string]any{"match_id": match.MatchID, "ts": match.ReceivedAt.UnixMilli()},
	})
	// Wake analytics-engine-py, which SUBSCRIBEs to this channel. XADD alone
	// does not deliver to Pub/Sub subscribers.
	pipe.Publish(ctx, channelKey, frame)
	_, err = pipe.Exec(ctx)
	return err
}

// Close terminates background activity and the underlying connection pool.
// It is idempotent: subsequent calls are no-ops returning nil.
func (r *Redis) Close() (err error) {
	r.once.Do(func() {
		close(r.done)
		err = r.client.Close()
	})
	return err
}

// winnerOf returns the current bookmaker-favoured side of a 1X2 market.
func winnerOf(m stream.Market) string {
	if m.Odds.Home <= m.Odds.Draw && m.Odds.Home <= m.Odds.Away {
		return "home"
	}
	if m.Odds.Away <= m.Odds.Draw {
		return "away"
	}
	return "draw"
}