package betpawa

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/casuya-live/data-ingestion/internal/stream"
)

type Config struct {
	BaseURL    string
	Brand      string
	Language   string
	MarketType string
	Take       int
	Interval   time.Duration
}

func defaultConfig() Config {
	return Config{
		BaseURL:    "https://www.betpawa.co.tz",
		Brand:      "betpawa-tanzania",
		Language:   "en",
		MarketType: "3743", // 1X2-FT
		Take:       100,
		Interval:   5 * time.Second,
	}
}

// PublishFunc is the sink receiving validated pipeline match frames.
type PublishFunc func(ctx context.Context, m stream.Match) error

type tracked struct {
	match     stream.Match
	lastSeen  time.Time
	ftEmitted bool
}

type Poller struct {
	cfg     Config
	client  *http.Client
	active  map[string]*tracked
	publish PublishFunc
}

func New(cfg Config) *Poller {
	def := defaultConfig()
	if cfg.BaseURL != "" {
		def.BaseURL = cfg.BaseURL
	}
	if cfg.Brand != "" {
		def.Brand = cfg.Brand
	}
	if cfg.Language != "" {
		def.Language = cfg.Language
	}
	if cfg.MarketType != "" {
		def.MarketType = cfg.MarketType
	}
	if cfg.Take > 0 {
		def.Take = cfg.Take
	}
	if cfg.Interval > 0 {
		def.Interval = cfg.Interval
	}
	return &Poller{
		cfg:    def,
		client: &http.Client{ Timeout: 20 * time.Second },
		active: make(map[string]*tracked),
	}
}

func (p *Poller) Run(ctx context.Context, publish PublishFunc) error {
	p.publish = publish
	interval := p.cfg.Interval
	log.Printf("[betpawa] polling live feed every %s (base=%s brand=%s take=%d)",
		interval, p.cfg.BaseURL, p.cfg.Brand, p.cfg.Take)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := p.tick(ctx); err != nil {
			log.Printf("[betpawa] tick error: %v", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			continue
		}
	}
}

func (p *Poller) tick(ctx context.Context) error {
	events, err := p.fetchLive(ctx)
	if err != nil {
		return err
	}

	nowSeen := make(map[string]bool)
	for _, ev := range events {
		m, ok := p.toMatch(ev)
		if !ok {
			continue
		}
		nowSeen[m.MatchID] = true
		trk, exists := p.active[m.MatchID]
		if !exists {
			trk = &tracked{match: m}
			p.active[m.MatchID] = trk
		}
		trk.match = m
		trk.lastSeen = time.Now()
		p.publish(ctx, m)

		if !trk.ftEmitted && isFullTimePeriod(ev.Results.Display) {
			trk.ftEmitted = true
			ft := m
			ft.Clock = "FULLTIME"
			p.publish(ctx, ft)
		}
	}

	for id, trk := range p.active {
		if !nowSeen[id] {
			if !trk.ftEmitted && clockIsLate(trk.match.Clock) {
				ft := trk.match
				ft.Clock = "FULLTIME"
				ft.ReceivedAt = time.Now().UTC()
				p.publish(ctx, ft)
				log.Printf("[betpawa] dropped %s — emitted FULLTIME %d-%d", id, ft.Score.Home, ft.Score.Away)
			} else if !trk.ftEmitted {
				log.Printf("[betpawa] dropped %s from live feed (clock %s, result pending executor confirmation)", id, trk.match.Clock)
			}
			delete(p.active, id)
		}
	}
	return nil
}

func (p *Poller) fetchLive(ctx context.Context) ([]rawEvent, error) {
	query := fmt.Sprintf(
		`{"queries":[{"query":{"eventType":"LIVE","categories":["2"],"zones":{}},"view":{"marketTypes":["%s"]},"skip":0,"take":%d,"sort":{"competitionPriority":"DESC"}}]}`,
		p.cfg.MarketType, p.cfg.Take,
	)
	reqURL := p.cfg.BaseURL + "/api/sportsbook/v4/events/lists/by-queries?q=" + url.QueryEscape(query)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil { return nil, err }
	req.Header.Set("Accept", "application/json")
	req.Header.Set("x-pawa-brand", p.cfg.Brand)
	req.Header.Set("x-pawa-language", p.cfg.Language)
	req.Header.Set("devicetype", "web")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) Chrome/126.0.0.0 Safari/537.36")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Referer", p.cfg.BaseURL+"/events/live")

	resp, err := p.client.Do(req)
	if err != nil { return nil, fmt.Errorf("request failed: %w", err) }
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20)) // 2MB cap
	if err != nil { return nil, err }
	events, ok := parseLiveEvents(body)
	if !ok { return nil, fmt.Errorf("parse error") }
	return events, nil
}

func (p *Poller) toMatch(ev rawEvent) (stream.Match, bool) {
	if !strings.EqualFold(ev.Category.Name, "Football") {
		return stream.Match{}, false
	}
	odds, ok := ftOdds(ev.Markets)
	if !ok { return stream.Match{}, false }
	home, away := scoreParticipants(ev.Results.ParticipantPeriodResults)

	var homeName, awayName string
	for _, pt := range ev.Participants {
		if pt.Position == 1 { homeName = pt.Name }
		if pt.Position == 2 { awayName = pt.Name }
	}

	return stream.Match{
		MatchID:    "bp-" + ev.ID,
		Sport:      ev.Category.Name,
		League:     ev.Region.Name + ". " + ev.Competition.Name,
		HomeTeam:   homeName,
		AwayTeam:   awayName,
		Kickoff:    kickoffTime(ev.StartTime).UTC(),
		Clock:      parseClock(ev.Results.Display),
		Score:      stream.Score{ Home: home, Away: away },
		Markets: map[string]stream.Market{
			stream.MarketFT1X2: {
				MarketID:  stream.MarketFT1X2,
				Odds:      odds,
				UpdatedAt: time.Now().UnixMilli(),
			},
		},
		ReceivedAt: time.Now().UTC(),
	}, true
}
