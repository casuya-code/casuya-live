package helabet

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/casuya-live/data-ingestion/internal/stream"
)

// Config parameterises the EveryMatrix public feed client.
// The defaults below are the exact values Helabet's own SPA sends.
type Config struct {
	BaseURL    string        // e.g. https://helabet.co.tz
	Ref        int           // 237 (client id)
	Gr         int           // 772 (betting group id)
	Lng        string        // "en"
	CountryID  int           // fcountry, 181 = Tanzania filter
	Count      int           // how many live matches to request
	Interval   time.Duration // poll cadence
	HTTPClient *http.Client
}

func (c Config) withDefaults() Config {
	if c.BaseURL == "" {
		c.BaseURL = "https://helabet.co.tz"
	}
	if c.Ref == 0 {
		c.Ref = 237
	}
	if c.Gr == 0 {
		c.Gr = 772
	}
	if c.Lng == "" {
		c.Lng = "en"
	}
	if c.CountryID == 0 {
		c.CountryID = 181
	}
	if c.Count == 0 {
		c.Count = 50 // the EveryMatrix gateway rejects count > ~50 with 400
	}
	if c.Interval == 0 {
		c.Interval = 5 * time.Second
	}
	if c.HTTPClient == nil {
		c.HTTPClient = &http.Client{Timeout: 15 * time.Second}
	}
	return c
}

// PublishFunc is the sink receiving validated pipeline match frames.
type PublishFunc func(ctx context.Context, m stream.Match) error

// Poller watches Helabet's live 1X2 feed, republishes real matches into the
// CASUYA-LIVE pipeline, and emits FULLTIME settlement frames when a tracked
// match completes.
type Poller struct {
	cfg     Config
	client  *http.Client
	publish PublishFunc
	mu      sync.Mutex
	active  map[string]*trackedMatch
	settled map[string]bool
}

type trackedMatch struct {
	raw       *rawMatch
	lastFrame stream.Match
}

// New constructs the poller with validated configuration.
func New(cfg Config) *Poller {
	cfg = cfg.withDefaults()
	return &Poller{
		cfg:     cfg,
		client:  cfg.HTTPClient,
		active:  make(map[string]*trackedMatch),
		settled: make(map[string]bool),
	}
}

// Run starts the poll loop; it returns only when ctx is cancelled.
func (p *Poller) Run(ctx context.Context, publish PublishFunc) error {
	p.publish = publish
	interval := p.cfg.Interval
	log.Printf("[helabet] polling live feed every %s (base=%s ref=%d gr=%d lng=%s fcountry=%d)",
		interval, p.cfg.BaseURL, p.cfg.Ref, p.cfg.Gr, p.cfg.Lng, p.cfg.CountryID)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Initial fetch immediately so the desk has frames fast.
	p.tick(ctx)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			p.tick(ctx)
		}
	}
}

func (p *Poller) tick(ctx context.Context) {
	raws, err := p.fetchLive(ctx)
	if err != nil {
		log.Printf("[helabet] live feed fetch failed: %v", err)
		return
	}
	if len(raws) == 0 {
		log.Printf("[helabet] live feed returned no matches")
	}

	seen := make(map[string]bool, len(raws))
	for i := range raws {
		raw := &raws[i]
		if raw.ID == 0 || raw.Opponent1.FullName == "" || raw.Opponent2.FullName == "" {
			continue
		}
		matchID := matchIDKey(raw.ID)
		seen[matchID] = true

		p.mu.Lock()
		alreadySettled := p.settled[matchID]
		p.mu.Unlock()
		if alreadySettled {
			continue
		}

		frame, ok := p.toMatch(raw)
		if !ok {
			continue // no valid FULLTIME_1X2 market — not bettable for our scope
		}

		p.mu.Lock()
		p.active[matchID] = &trackedMatch{raw: raw, lastFrame: frame}
		p.mu.Unlock()

		if IsFullTime(raw) {
			// The feed surfaced a finished entry; settle and stop tracking.
			ft := frame
			ft.Clock = "FULLTIME"
			ft.ReceivedAt = time.Now().UTC()
			if err := p.safePublish(ctx, ft); err != nil {
				log.Printf("[helabet] FULLTIME publish %s failed: %v", matchID, err)
			}
			p.markSettled(matchID)
			continue
		}

		if err := p.safePublish(ctx, frame); err != nil {
			log.Printf("[helabet] live publish %s failed: %v", matchID, err)
		}
	}

	// Drop tracked matches that vanished from the live feed. Their orders are
	// left for the executor's stale sweep, which resolves them only against a
	// confirmed FULLTIME frame (or marks them unresolved). We never fabricate a
	// full-time score from a frozen live frame here.
	p.mu.Lock()
	var dropped []string
	for id := range p.active {
		if seen[id] {
			continue
		}
		dropped = append(dropped, id)
	}
	for _, id := range dropped {
		delete(p.active, id)
	}
	p.mu.Unlock()

	for _, id := range dropped {
		p.markSettled(id)
		log.Printf("[helabet] dropped %s from live feed (result pending executor confirmation)", id)
	}
}

func (p *Poller) markSettled(matchID string) {
	p.mu.Lock()
	p.settled[matchID] = true
	delete(p.active, matchID)
	p.mu.Unlock()
}

func (p *Poller) safePublish(ctx context.Context, m stream.Match) error {
	if p.publish == nil {
		return nil
	}
	return p.publish(ctx, m)
}

// fetchLive pulls the current live 1X2 board from the book's public feed.
func (p *Poller) fetchLive(ctx context.Context) ([]rawMatch, error) {
	u := p.cfg.BaseURL + "/service-api/main-live-feed/v3/games1x2"
	q := url.Values{}
	q.Set("cfView", "3")
	q.Set("count", strconv.Itoa(p.cfg.Count))
	q.Set("fcountry", strconv.Itoa(p.cfg.CountryID))
	q.Set("gr", strconv.Itoa(p.cfg.Gr))
	q.Set("grMode", "4")
	q.Set("lng", p.cfg.Lng)
	q.Set("ref", strconv.Itoa(p.cfg.Ref))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u+"?"+q.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	p.decorate(req, "__BETTING_APP__")
	req.Header.Set("Referer", p.cfg.BaseURL+"/en")

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("get live feed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("live feed status %d", resp.StatusCode)
	}

	var raws []rawMatch
	if err := json.NewDecoder(resp.Body).Decode(&raws); err != nil {
		return nil, fmt.Errorf("decode live feed: %w", err)
	}
	return raws, nil
}

// decorate stamps the app-source headers the vendor gateway requires.
func (p *Poller) decorate(req *http.Request, app string) {
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0 Safari/537.36")
	req.Header.Set("x-svc-source", app)
	req.Header.Set("x-app-n", app)
	req.Header.Set("is-srv", "false")
	req.Header.Set("x-requested-with", "XMLHttpRequest")
}

// toMatch projects a raw live fixture onto the pipeline frame contract.
// It returns ok=false when the fixture lacks a valid FULLTIME_1X2 market.
func (p *Poller) toMatch(raw *rawMatch) (stream.Match, bool) {
	// The momentum model is football-only; drop everything else at the gate.
	if raw.Sport.ID != 1 {
		return stream.Match{}, false
	}
	odds, ok := ftOdds(raw.EventGroups)
	if !ok {
		return stream.Match{}, false
	}

	home, away := 0, 0
	if raw.Scores != nil {
		home = raw.Scores.ScoreOpp1
		away = raw.Scores.ScoreOpp2
	}

	return stream.Match{
		MatchID:  matchIDKey(raw.ID),
		Sport:    nonEmpty(raw.Sport.Name, "football"),
		League:   raw.Liga.Name,
		HomeTeam: raw.Opponent1.FullName,
		AwayTeam: raw.Opponent2.FullName,
		Kickoff:  time.Unix(raw.StartTs, 0).UTC(),
		Clock:    ParseClock(statusOf(raw), timerOf(raw)),
		Score:    stream.Score{Home: home, Away: away},
		Markets: map[string]stream.Market{
			stream.MarketFT1X2: {
				MarketID:  stream.MarketFT1X2,
				Odds:      odds,
				UpdatedAt: time.Now().UnixMilli(),
			},
		},
		Shots:            shotsOf(raw),
		ShotsOnTarget:    shotsOnTargetOf(raw),
		DangerousAttacks: dangerousAttacksOf(raw),
		PossessionHome:   possessionHomeOf(raw),
		ReceivedAt:       time.Now().UTC(),
	}, true
}

// ftOdds extracts the full-time 1X2 legs (home/draw/away) from eventGroups.
// The vendor encodes groupId=1 as the three-way main market with types 1/2/3.
func ftOdds(groups []eventGroup) (stream.OddsSet, bool) {
	var threeWay []event
	for _, g := range groups {
		if g.GroupID != 1 {
			continue
		}
		for _, nested := range g.Events {
			threeWay = append(threeWay, nested...)
		}
		if len(threeWay) >= 3 {
			break
		}
	}
	var out stream.OddsSet
	for _, e := range threeWay {
		if e.Cf <= 0 {
			return stream.OddsSet{}, false
		}
		switch e.Type {
		case 1:
			out.Home = e.Cf
		case 2:
			out.Draw = e.Cf
		case 3:
			out.Away = e.Cf
		}
	}
	if out.Home <= 0 || out.Draw <= 0 || out.Away <= 0 {
		return stream.OddsSet{}, false
	}
	return out, true
}

// ---- stats extraction (home legs only; the model reads home-vs-away gaps) ----

func statsOf(raw *rawMatch) []statItem {
	if raw.Scores == nil || raw.Scores.TabloStats == nil {
		return nil
	}
	return raw.Scores.TabloStats.Stats0
}

func statByID(items []statItem, id int) (float64, bool) {
	for _, it := range items {
		if it.ID != id {
			continue
		}
		v, err := strconv.ParseFloat(strings.TrimSpace(it.S1), 64)
		if err != nil {
			return 0, false
		}
		return v, true
	}
	return 0, false
}

func possessionHomeOf(raw *rawMatch) float64 {
	if v, ok := statByID(statsOf(raw), 29); ok {
		return v
	}
	return 0
}

func shotsOnTargetOf(raw *rawMatch) int {
	items := statsOf(raw)
	on, ok1 := statByID(items, 59)
	off, ok2 := statByID(items, 60)
	if !ok1 && !ok2 {
		return 0
	}
	onF, offF := 0.0, 0.0
	if ok1 {
		onF = on
	}
	if ok2 {
		offF = off
	}
	return int(onF + offF)
}

func shotsOf(raw *rawMatch) int {
	items := statsOf(raw)
	total := 0.0
	for _, id := range []int{59, 60} {
		if v, ok := statByID(items, id); ok {
			total += v
		}
	}
	return int(total)
}

func dangerousAttacksOf(raw *rawMatch) int {
	if v, ok := statByID(statsOf(raw), 58); ok {
		return int(v)
	}
	return 0
}

func statusOf(raw *rawMatch) string {
	if raw.Scores == nil {
		return ""
	}
	return raw.Scores.StatusLineStr
}

func timerOf(raw *rawMatch) *matchTimer {
	if raw.Scores == nil {
		return nil
	}
	return raw.Scores.Timer
}

func matchIDKey(id int) string {
	return "hb-" + strconv.Itoa(id)
}

func nonEmpty(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}