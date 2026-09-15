package helabet

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/casuya-live/data-ingestion/internal/stream"
)

func TestParseClock(t *testing.T) {
	cases := []struct {
		in   string
		tmr  *matchTimer
		want string
	}{
		{"6 minutes", nil, "6'"},
		{"93 minutes", nil, "93'"},
		{"HT", nil, "HT"},
		{"HalfTime", nil, "HT"},
		{"Match finished", nil, "FULLTIME"},
		{"FULL TIME 2-1", nil, "FULLTIME"},
		{"Event in progress", &matchTimer{TimeSec: 3000}, "50'"},
		{"", &matchTimer{TimeSec: 1200}, "20'"},
		{"", nil, "LIVE"},
	}
	for _, c := range cases {
		if got := ParseClock(c.in, c.tmr); got != c.want {
			t.Errorf("ParseClock(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestIsFullTime(t *testing.T) {
	live := &rawMatch{Scores: &scores{
		StatusLineStr: "65 minutes",
		CurrentPeriod: 2,
		Timer:         &matchTimer{TimeSec: 3900, TimeRun: true},
	}}
	if IsFullTime(live) {
		t.Fatal("in-play match misclassified as finished")
	}
	finished := &rawMatch{Scores: &scores{StatusLineStr: "Match finished", CurrentPeriod: 2}}
	if !IsFullTime(finished) {
		t.Fatal("explicit finished marker not detected")
	}
	stoppedTimer := &rawMatch{Scores: &scores{
		CurrentPeriod: 2,
		Timer:         &matchTimer{TimeSec: 5599, TimeRun: false},
	}}
	if !IsFullTime(stoppedTimer) {
		t.Fatal("stopped timer after full time not detected")
	}
	halfStop := &rawMatch{Scores: &scores{
		CurrentPeriod: 1,
		Timer:         &matchTimer{TimeSec: 1200, TimeRun: false},
	}}
	if IsFullTime(halfStop) {
		t.Fatal("half-time stop misclassified as finished")
	}
}

func TestFtEligibleForSettlement(t *testing.T) {
	if ftEligibleForSettlement(&rawMatch{}) {
		t.Fatal("empty match should not be settlement eligible")
	}
	early := &rawMatch{Scores: &scores{CurrentPeriod: 2, Timer: &matchTimer{TimeSec: 4000}}}
	if ftEligibleForSettlement(early) {
		t.Fatal("60' match should not be settlement eligible")
	}
	late := &rawMatch{Scores: &scores{CurrentPeriod: 2, Timer: &matchTimer{TimeSec: 5400}}}
	if !ftEligibleForSettlement(late) {
		t.Fatal("90' match should be settlement eligible")
	}
}

func TestFtOdds(t *testing.T) {
	groups := []eventGroup{
		{
			GroupID: 1,
			Events: [][]event{
				{{Type: 1, Cf: 1.57}},
				{{Type: 2, Cf: 4.38}},
				{{Type: 3, Cf: 5.3}},
			},
		},
		{GroupID: 8, Events: [][]event{{{Type: 4, Cf: 1.16}}}},
	}
	odds, ok := ftOdds(groups)
	if !ok {
		t.Fatal("expected valid odds set")
	}
	if odds.Home != 1.57 || odds.Draw != 4.38 || odds.Away != 5.3 {
		t.Fatalf("odds mismatch: %+v", odds)
	}

	if _, ok := ftOdds(nil); ok {
		t.Fatal("empty groups should fail")
	}
	incomplete := []eventGroup{{GroupID: 1, Events: [][]event{{{Type: 1, Cf: 1.57}}}}}
	if _, ok := ftOdds(incomplete); ok {
		t.Fatal("missing legs should fail")
	}
	zeroOdds := []eventGroup{{GroupID: 1, Events: [][]event{
		{{Type: 1, Cf: 0}}, {{Type: 2, Cf: 4}}, {{Type: 3, Cf: 5}},
	}}}
	if _, ok := ftOdds(zeroOdds); ok {
		t.Fatal("zero odds should fail")
	}
}

func TestToMatch(t *testing.T) {
	raw := &rawMatch{
		ID:        753077051,
		Sport:     sportRef{ID: 1, Name: "Football"},
		Liga:      champRef{ID: 2740174, Name: "AFC Champions League Elite"},
		Opponent1: teamRef{FullName: "Beijing Guoan"},
		Opponent2: teamRef{FullName: "Pohang Steelers"},
		StartTs:   1789474700,
		Scores: &scores{
			FullScore:     "1-1",
			ScoreOpp1:     1,
			ScoreOpp2:     1,
			StatusLineStr: "6 minutes",
			CurrentPeriod: 1,
			Timer:         &matchTimer{TimeSec: 402, TimeRun: true},
			TabloStats: &tabloStats{Stats0: []statItem{
				{ID: 29, Name: "Possession %", S1: "33", S2: "67"},
				{ID: 45, Name: "Attacks", S1: "5", S2: "7"},
				{ID: 58, Name: "Dangerous attacks", S1: "1", S2: "2"},
				{ID: 59, Name: "Shots on target", S1: "1", S2: "1"},
				{ID: 60, Name: "Shots off target", S1: "0", S2: "0"},
			}},
		},
		EventGroups: []eventGroup{{
			GroupID: 1,
			Events: [][]event{
				{{Type: 1, Cf: 1.57}},
				{{Type: 2, Cf: 4.38}},
				{{Type: 3, Cf: 5.3}},
			},
		}},
	}

	p := New(Config{})
	m, ok := p.toMatch(raw)
	if !ok {
		t.Fatal("toMatch should succeed")
	}
	if m.MatchID != "hb-753077051" {
		t.Errorf("match id = %q", m.MatchID)
	}
	if m.HomeTeam != "Beijing Guoan" || m.AwayTeam != "Pohang Steelers" {
		t.Errorf("teams wrong: %s vs %s", m.HomeTeam, m.AwayTeam)
	}
	if m.League != "AFC Champions League Elite" {
		t.Errorf("league = %q", m.League)
	}
	if m.Clock != "6'" {
		t.Errorf("clock = %q", m.Clock)
	}
	if m.Score.Home != 1 || m.Score.Away != 1 {
		t.Errorf("score = %d-%d", m.Score.Home, m.Score.Away)
	}
	if m.PossessionHome != 33 {
		t.Errorf("possession = %v", m.PossessionHome)
	}
	if m.DangerousAttacks != 1 {
		t.Errorf("dangerous attacks = %d", m.DangerousAttacks)
	}
	if m.ShotsOnTarget != 1 || m.Shots != 1 {
		t.Errorf("shots on=%d total=%d", m.ShotsOnTarget, m.Shots)
	}
	mkt, have := m.Markets[stream.MarketFT1X2]
	if !have {
		t.Fatal("missing FULLTIME_1X2 market")
	}
	if mkt.Odds.Home != 1.57 || mkt.Odds.Draw != 4.38 || mkt.Odds.Away != 5.3 {
		t.Errorf("market odds wrong: %+v", mkt.Odds)
	}
	if !strings.HasPrefix(m.Kickoff.String(), "2026") {
		t.Errorf("kickoff = %v", m.Kickoff)
	}

	noMarket := &rawMatch{ID: 1, Opponent1: teamRef{FullName: "A"}, Opponent2: teamRef{FullName: "B"}}
	if _, ok := p.toMatch(noMarket); ok {
		t.Fatal("match without 1X2 market should be dropped")
	}

	nonFootball := &rawMatch{
		ID: 2, Sport: sportRef{ID: 66, Name: "Cricket"},
		Opponent1: teamRef{FullName: "Zimbabwe"}, Opponent2: teamRef{FullName: "Australia"},
		EventGroups: raw.EventGroups,
	}
	if _, ok := p.toMatch(nonFootball); ok {
		t.Fatal("non-football match should be dropped")
	}
}

func TestFetchLive(t *testing.T) {
	payload := []rawMatch{{
		ID:        42,
		Sport:     sportRef{ID: 1, Name: "Football"},
		Liga:      champRef{ID: 7, Name: "Test League"},
		Opponent1: teamRef{FullName: "Home FC"},
		Opponent2: teamRef{FullName: "Away FC"},
		StartTs:   1789470000,
		Scores: &scores{
			FullScore:     "2-0",
			ScoreOpp1:     2,
			ScoreOpp2:     0,
			StatusLineStr: "12 minutes",
			CurrentPeriod: 1,
			Timer:         &matchTimer{TimeSec: 720, TimeRun: true},
		},
		EventGroups: []eventGroup{{GroupID: 1, Events: [][]event{
			{{Type: 1, Cf: 2.0}}, {{Type: 2, Cf: 3.5}}, {{Type: 3, Cf: 3.6}},
		}}},
	}}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		if got := r.Header.Get("x-svc-source"); got != "__BETTING_APP__" {
			t.Errorf("x-svc-source = %q", got)
		}
		if q := r.URL.Query(); q.Get("gr") != "772" || q.Get("ref") != "237" || q.Get("lng") != "en" || q.Get("cfView") != "3" {
			t.Errorf("params = %v", q)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(payload)
	}))
	defer srv.Close()

	p := New(Config{BaseURL: srv.URL})
	raws, err := p.fetchLive(context.Background())
	if err != nil {
		t.Fatalf("fetchLive: %v", err)
	}
	if len(raws) != 1 || raws[0].ID != 42 || raws[0].Scores.FullScore != "2-0" {
		t.Fatalf("decoded wrong: %+v", raws)
	}
}

func TestFetchLiveRejectsNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	p := New(Config{BaseURL: srv.URL})
	if _, err := p.fetchLive(context.Background()); err == nil {
		t.Fatal("expected error on non-200")
	}
}

func TestRunSettlesDroppedMatch(t *testing.T) {
	fixture := func(secs int) rawMatch {
		return rawMatch{
			ID:        123,
			Sport:     sportRef{ID: 1, Name: "Football"},
			Liga:      champRef{ID: 1, Name: "League"},
			Opponent1: teamRef{FullName: "A"},
			Opponent2: teamRef{FullName: "B"},
			StartTs:   1789470000,
			Scores: &scores{
				FullScore:     "1-0",
				ScoreOpp1:     1,
				ScoreOpp2:     0,
				StatusLineStr: "90 minutes",
				CurrentPeriod: 2,
				Timer:         &matchTimer{TimeSec: secs, TimeRun: true},
			},
			EventGroups: []eventGroup{{GroupID: 1, Events: [][]event{
				{{Type: 1, Cf: 2.0}}, {{Type: 2, Cf: 3.5}}, {{Type: 3, Cf: 3.6}},
			}}},
		}
	}

	// Empty feed server: the tracked match drops off -> must settle.
	empty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("[]"))
	}))
	defer empty.Close()

	p := New(Config{BaseURL: empty.URL})

	// Seed the internal tracker directly (as if a live frame was seen at 90'+).
	seed := fixture(5410)
	frame, ok := p.toMatch(&seed)
	if !ok {
		t.Fatal("seed frame failed to map")
	}
	p.mu.Lock()
	p.active["hb-123"] = &trackedMatch{raw: &seed, lastFrame: frame}
	p.mu.Unlock()

	var frames []stream.Match
	p.publish = func(_ context.Context, m stream.Match) error {
		frames = append(frames, m)
		return nil
	}

	p.tick(context.Background())

	found := false
	for _, f := range frames {
		if f.Clock == "FULLTIME" && f.MatchID == "hb-123" {
			found = true
			if f.Score.Home != 1 || f.Score.Away != 0 {
				t.Errorf("settlement score = %d-%d", f.Score.Home, f.Score.Away)
			}
		}
	}
	if !found {
		t.Errorf("expected FULLTIME settlement frame, got %d frames", len(frames))
	}
}

func TestStaleActivePurge(t *testing.T) {
	p := New(Config{})
	old := &trackedMatch{raw: &rawMatch{}, lastFrame: stream.Match{ReceivedAt: time.Now().UTC().Add(-8 * time.Hour)}}
	p.mu.Lock()
	p.active["hb-999"] = old
	p.mu.Unlock()

	seed := rawMatch{
		ID:        100,
		Sport:     sportRef{ID: 1, Name: "Football"},
		Liga:      champRef{ID: 1, Name: "L"},
		Opponent1: teamRef{FullName: "AA"},
		Opponent2: teamRef{FullName: "BB"},
		Scores: &scores{
			ScoreOpp1:     0,
			ScoreOpp2:     0,
			StatusLineStr: "10 minutes",
			CurrentPeriod: 1,
			Timer:         &matchTimer{TimeSec: 600, TimeRun: true},
		},
		EventGroups: []eventGroup{{GroupID: 1, Events: [][]event{
			{{Type: 1, Cf: 2.0}}, {{Type: 2, Cf: 3.5}}, {{Type: 3, Cf: 3.6}},
		}}},
	}

	var frames []stream.Match
	p.publish = func(_ context.Context, m stream.Match) error {
		frames = append(frames, m)
		return nil
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]rawMatch{seed})
	}))
	defer srv.Close()
	p.cfg.BaseURL = srv.URL
	p.cfg.HTTPClient = srv.Client()

	p.tick(context.Background())

	p.mu.Lock()
	defer p.mu.Unlock()
	if _, still := p.active["hb-999"]; still {
		t.Error("stale match should have been purged")
	}
	// Old entry must not be settled (never reached full time), and the new live
	// frame must have been published.
	settledOld := false
	for _, f := range frames {
		if f.MatchID == "hb-999" && f.Clock == "FULLTIME" {
			settledOld = true
		}
	}
	if settledOld {
		t.Error("stale pre-fulltime match should not emit settlement")
	}
	if len(frames) == 0 || frames[len(frames)-1].MatchID != "hb-100" {
		t.Errorf("expected live publish of new match, frames=%+v", frames)
	}
}