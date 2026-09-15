// Package apifootball streams real live in-play football data from the
// API-Football (apiv3.apifootball.com) legacy HTTP API into the CASUYA-LIVE
// match contract.
//
// The free plan covers England Championship and France Ligue 2 with live
// events (clock, score, half-time score, possession, shots, dangerous
// attacks), live 1X2 odds, and final results — the exact surface the
// momentum model and the FULLTIME settlement path consume.
package apifootball

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/casuya-live/data-ingestion/internal/stream"
)

const defaultBaseURL = "https://apiv3.apifootball.com"

// Poller polls the API-Football live endpoints and feeds validated match
// frames into the same sink (Redis) that the mock WebSocket provider uses.
type Poller struct {
	client   *http.Client
	key      string
	base     string
	interval time.Duration
	league   string // optional substring filter on league_name
	playing  map[string]*matchState
}

// matchState retains a live match between poll cycles so finished matches can
// settle with their last known closing odds.
type matchState struct {
	Home, Away, League string
	Kickoff            int64
	LastMarket         stream.Market
	LastScore          score
}

type score struct{ Home, Away int }

// New builds a Poller for the given API-Football key. interval paces poll
// cycles; the free plan's "180 calls per hour & endpoint" ceiling means the
// caller should keep intervals >= 20s.
func New(ctx context.Context, key string, interval time.Duration) (*Poller, error) {
	base := os.Getenv("APIFOOTBALL_BASE_URL")
	if base == "" {
		base = defaultBaseURL
	}
	p := &Poller{
		client:   &http.Client{Timeout: 20 * time.Second},
		key:      key,
		base:     strings.TrimRight(base, "/"),
		interval: interval,
		league:   os.Getenv("APIFOOTBALL_LEAGUE_FILTER"),
		playing:  map[string]*matchState{},
	}
	// One mandatory live-licence check: a bad key fails fast at startup.
	if _, _, err := p.fetch(ctx, url.Values{"action": {"get_live_odds_commnets"}}); err != nil {
		return nil, err
	}
	return p, nil
}

// Run blocks until ctx is cancelled, polling live events + odds each cycle and
// emitting validated match frames (plus FULLTIME settlement frames) to sink.
func (p *Poller) Run(ctx context.Context, sink stream.Sink) error {
	if err := p.cycle(ctx, sink); err != nil {
		log.Printf("[apifootball] initial cycle failed: %v", err)
	}
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := p.cycle(ctx, sink); err != nil {
				log.Printf("[apifootball] cycle failed: %v", err)
			}
		}
	}
}

// fetch performs one GET against the legacy endpoint and returns (body, status).
func (p *Poller) fetch(ctx context.Context, q url.Values) ([]byte, int, error) {
	q.Set("APIkey", p.key)
	u := p.base + "/?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, 0, err
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	if resp.StatusCode != http.StatusOK {
		return body, resp.StatusCode, nil
	}
	return body, resp.StatusCode, nil
}

// cycle fetches live events, live odds and today's fixtures/standings, then
// emits live frames for in-play matches and FULLTIME frames for those that
// finished since the last cycle.
func (p *Poller) cycle(ctx context.Context, sink stream.Sink) error {
	type fetchResult struct {
		kind string
		raw  []byte
	}
	results := make(chan fetchResult, 3)
	fetchOne := func(kind string, q url.Values) {
		raw, code, err := p.fetch(ctx, q)
		if err != nil || code != http.StatusOK {
			log.Printf("[apifootball] %s fetch failed (code %d): %v", kind, code, err)
			results <- fetchResult{kind: kind}
			return
		}
		results <- fetchResult{kind: kind, raw: raw}
	}

	// Fire the three reads concurrently; a stalled or rate-limited endpoint
	// must not block the others. Free plan: one call per endpoint per cycle.
	go fetchOne("live_odds", url.Values{"action": {"get_live_odds_commnets"}})
	go fetchOne("live_events", url.Values{"action": {"get_events"}, "match_live": {"1"}})
	today := time.Now().Format("2006-01-02")
	go fetchOne("day_events", url.Values{"action": {"get_events"}, "from": {today}, "to": {today}})

	odds := map[string][3]float64{}
	live := map[string]*liveEvent{}
	day := map[string]*liveEvent{}
	for range 3 {
		res := <-results
		if len(res.raw) == 0 {
			continue
		}
		switch res.kind {
		case "live_odds":
			odds = parseLiveOdds(res.raw)
		case "live_events":
			live = parseEvents(res.raw)
		case "day_events":
			day = parseEvents(res.raw)
		}
	}

	// Emit live frames for in-play matches with real-time odds.
	for id, ev := range live {
		if !ev.inPlay() {
			continue
		}
		st := p.playing[id]
		if st == nil {
			st = &matchState{
				Home: ev.Home, Away: ev.Away, League: ev.League, Kickoff: ev.Kickoff,
			}
			p.playing[id] = st
		}
		minute := ev.minute()
		if minute == "" {
			continue
		}
		o, ok := odds[id]
		if !ok {
			continue
		}
		mkt := stream.Market{MarketID: stream.MarketFT1X2, Odds: stream.OddsSet{Home: o[0], Draw: o[1], Away: o[2]}}
		if mkt.Validate() != nil {
			continue
		}
		st.LastMarket = mkt
		st.LastScore = score{ev.HomeScore, ev.AwayScore}
		match := stream.Match{
			MatchID:          "api-" + id,
			Sport:            "soccer",
			League:           ev.League,
			HomeTeam:         ev.Home,
			AwayTeam:         ev.Away,
			Kickoff:          timeUnixOrZero(ev.Kickoff),
			Clock:            minute,
			Score:            stream.Score{Home: ev.HomeScore, Away: ev.AwayScore},
			Markets:          map[string]stream.Market{stream.MarketFT1X2: mkt},
			Shots:            ev.Shots,
			ShotsOnTarget:    ev.ShotsOnTarget,
			DangerousAttacks: ev.DangerousAttacks,
			PossessionHome:   ev.PossessionHome,
			ReceivedAt:       time.Now().UTC(),
		}
		if err := match.Validate(); err == nil {
			if err := sink(ctx, match); err != nil {
				log.Printf("[apifootball] sink failed for %s: %v", match.MatchID, err)
			}
		}
	}

	// Settle matches we were tracking once their final score is known.
	for id, st := range p.playing {
		if global := live[id]; global != nil {
			continue // still live
		}
		dayEv := day[id]
		if dayEv == nil {
			// Dropped from both feeds a cycle early (right after FT the live
			// feed forgets the match); retry next cycle, never invent a score.
			if _, ok := day[id]; !ok {
				continue
			}
			continue
		}
		final, ok := dayEv.finalScore()
		if !ok {
			continue
		}
		mkt := st.LastMarket
		if dayEv.hasOdds() {
			mkt = stream.Market{
				MarketID: stream.MarketFT1X2,
				Odds:     stream.OddsSet{Home: dayEv.ClosedHome, Draw: dayEv.ClosedDraw, Away: dayEv.ClosedAway},
			}
		}
		frame := stream.Match{
			MatchID:    "api-" + id,
			Sport:      "soccer",
			League:     st.League,
			HomeTeam:   st.Home,
			AwayTeam:   st.Away,
			Kickoff:    timeUnixOrZero(st.Kickoff),
			Clock:      "FULLTIME",
			Score:      stream.Score{Home: final[0], Away: final[1]},
			Markets:    map[string]stream.Market{stream.MarketFT1X2: mkt},
			ReceivedAt: time.Now().UTC(),
		}
		if frame.Validate() == nil {
			if err := sink(ctx, frame); err != nil {
				log.Printf("[apifootball] settlement sink failed for %s: %v", frame.MatchID, err)
			}
		}
		delete(p.playing, id)
	}

	// Register matches that appear live between cycles (skip armed events).
	for id, ev := range live {
		if ev.inPlay() {
			if _, ok := p.playing[id]; !ok {
				p.playing[id] = &matchState{Home: ev.Home, Away: ev.Away, League: ev.League, Kickoff: ev.Kickoff}
			}
		}
	}
	return nil
}

// liveEvent is one row of the get_events response.
type liveEvent struct {
	ID               string
	Home             string
	Away             string
	League           string
	Status           string
	MatchTime        string
	HomeScore        int
	AwayScore        int
	HTHome           int
	HTAway           int
	Shots            int
	ShotsOnTarget    int
	DangerousAttacks int
	PossessionHome   float64
	ClosedHome       float64
	ClosedDraw       float64
	ClosedAway       float64
	HasClosedOdds    bool
	Kickoff          int64
}

func (e *liveEvent) inPlay() bool {
	if e == nil {
		return false
	}
	switch strings.ToUpper(strings.TrimSpace(e.Status)) {
	case "1H", "2H", "ET", "AET":
		return true
	}
	return false
}

// minute extracts the match clock as "NN'" (or "NN+MM'"), or "" when unknown.
func (e *liveEvent) minute() string {
	if e == nil {
		return ""
	}
	m := strings.TrimSpace(e.MatchTime)
	if m == "" && e.inPlay() {
		// Some fixtures omit match_time for a moment at kick-off; emit only
		// when the clock is actually present so analytics never sees blank.
		return ""
	}
	return m
}

// finalScore returns (home, away) for finished matches.
func (e *liveEvent) finalScore() ([2]int, bool) {
	if e == nil {
		return [2]int{}, false
	}
	switch strings.ToUpper(strings.TrimSpace(e.Status)) {
	case "FT", "AET", "PEN":
		return [2]int{e.HomeScore, e.AwayScore}, true
	}
	return [2]int{}, false
}

func (e *liveEvent) hasOdds() bool { return e != nil && e.HasClosedOdds }

// eventRaw matches both the documented array shape and the {"results":[...]}
// wrapper some endpoints return.
type eventRaw struct {
	Results []json.RawMessage `json:"results"`
}

// parseEvents decodes the get_events payload into a lookup keyed by match id.
func parseEvents(raw []byte) map[string]*liveEvent {
	rows := []json.RawMessage{}
	if err := json.Unmarshal(raw, &rows); err != nil {
		var wrapped eventRaw
		if werr := json.Unmarshal(raw, &wrapped); werr != nil || len(wrapped.Results) == 0 {
			return map[string]*liveEvent{} // empty feeds return {} or [] legitimately
		}
		rows = wrapped.Results
	}
	out := map[string]*liveEvent{}
	for _, row := range rows {
		ev := decodeEvent(row)
		if ev != nil && ev.ID != "" {
			out[ev.ID] = ev
		}
	}
	return out
}

// decodeEvent maps one legacy event row to the internal representation.
func decodeEvent(row json.RawMessage) *liveEvent {
	var m map[string]any
	if err := json.Unmarshal(row, &m); err != nil {
		return nil
	}
	e := &liveEvent{
		ID:        s(m["match_id"]),
		Home:      s(m["match_hometeam_name"]),
		Away:      s(m["match_awayteam_name"]),
		League:    firstNonEmpty(s(m["league_name"]), s(m["country_name"])),
		Status:    strings.ToUpper(s(m["match_status"])),
		MatchTime: s(m["match_time"]),
		HomeScore: num(m["match_hometeam_score"]),
		AwayScore: num(m["match_awayteam_score"]),
		Kickoff:   i64(s(m["match_date"]) + " " + s(m["match_time"])),
	}
	if e.Home == "" || e.Away == "" || e.ID == "" {
		return nil
	}
	stats, _ := m["statistics"].([]any)
	for _, rowAny := range stats {
		row, ok := rowAny.(map[string]any)
		if !ok {
			continue
		}
		typ := strings.ToLower(s(row["type"]))
		switch {
		case strings.Contains(typ, "possession"):
			e.PossessionHome = percent(row["home"])
		case strings.Contains(typ, "shots total") || strings.Contains(typ, "total shots"):
			e.Shots = num(row["home"]) + num(row["away"])
		case strings.Contains(typ, "shots on") && strings.Contains(typ, "goal"):
			e.ShotsOnTarget = num(row["home"]) + num(row["away"])
		case strings.Contains(typ, "dangerous attack"):
			e.DangerousAttacks = num(row["home"]) + num(row["away"])
		}
	}
	return e
}

// oddRow is one bookmaker entry inside a live-odds match bucket.
type oddRow struct {
	Odd1 string `json:"odd_1"`
	OddX string `json:"odd_x"`
	Odd2 string `json:"odd_2"`
}

type liveOddsRaw map[string][]oddRow

// parseLiveOdds decodes the live-odds payload (keyed by match id) into median
// home/draw/away quotes per match.
func parseLiveOdds(raw []byte) map[string][3]float64 {
	var payload liveOddsRaw
	if err := json.Unmarshal(raw, &payload); err != nil {
		return map[string][3]float64{}
	}
	out := map[string][3]float64{}
	for id, rows := range payload {
		var home, draw, away []float64
		for _, r := range rows {
			h := f(r.Odd1)
			d := f(r.OddX)
			a := f(r.Odd2)
			if h > 0 {
				home = append(home, h)
			}
			if d > 0 {
				draw = append(draw, d)
			}
			if a > 0 {
				away = append(away, a)
			}
		}
		if len(home) == 0 || len(draw) == 0 || len(away) == 0 {
			continue
		}
		out[id] = [3]float64{median(home), median(draw), median(away)}
	}
	return out
}

// decodeEventOdds is unused directly; kept for parity with day-event closing odds.
var _ = decodeEventOdds

func decodeEventOdds(row json.RawMessage) *liveEvent {
	e := decodeEvent(row)
	if e == nil {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(row, &m); err != nil {
		return e
	}
	odds := s(m["odds_1"])
	o3 := s(m["odds_x"])
	o2 := s(m["odds_2"])
	if odds == "" && o3 == "" && o2 == "" {
		return e
	}
	h, d, a := f(odds), f(o3), f(o2)
	if h <= 0 || d <= 0 || a <= 0 {
		return e
	}
	e.ClosedHome, e.ClosedDraw, e.ClosedAway = h, d, a
	e.HasClosedOdds = true
	return e
}

// helpers for legacy stringly-typed payloads.
func s(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}

func num(v any) int {
	if f := f(v); f > 0 {
		return int(f)
	}
	return 0
}

func f(v any) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case string:
		out, err := strconv.ParseFloat(strings.TrimSpace(t), 64)
		if err != nil {
			return 0
		}
		return out
	}
	return 0
}

func percent(v any) float64 {
	raw := s(v)
	raw = strings.ReplaceAll(raw, "%", "")
	x, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil {
		return 0
	}
	return x / 100.0
}

func median(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	sort.Float64s(v)
	mid := len(v) / 2
	if len(v)%2 == 1 {
		return v[mid]
	}
	return (v[mid-1] + v[mid]) / 2.0
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func i64(v string) int64 {
	if t, err := time.Parse("2006-01-02 15:04", strings.TrimSpace(v)); err == nil {
		return t.Unix()
	}
	return 0
}

func timeUnixOrZero(unix int64) time.Time {
	if unix <= 0 {
		return time.Now().UTC().Add(-1 * time.Hour)
	}
	return time.Unix(unix, 0).UTC()
}