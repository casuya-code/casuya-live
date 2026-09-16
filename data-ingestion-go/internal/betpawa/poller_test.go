package betpawa

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/casuya-live/data-ingestion/internal/stream"
)

func TestParseLiveEvents(t *testing.T) {
	raw := []byte(`{"responses":[{"responses":[{"id":"12345","name":"A - B","startTime":"2026-09-15T12:00:00Z","results":{"display":{"minute":"33","currentPeriod":{"slug":"FIRST_HALF"}},"participantPeriodResults":[{"participant":{"type":"HOME"},"periodResults":[{"period":{"slug":"FULL_TIME_EXCLUDING_OVERTIME"},"result":"1","type":"SCORE"}]},{"participant":{"type":"AWAY"},"periodResults":[{"period":{"slug":"FULL_TIME_EXCLUDING_OVERTIME"},"result":"0","type":"SCORE"}]}]},"participants":[{"name":"Home","position":1},{"name":"Away","position":2}],"markets":[{"marketType":{"id":"3743"},"row":[{"prices":[{"name":"1","odds":2.1},{"name":"X","odds":3.2},{"name":"2","odds":4.0}]}]}],"category":{"id":"2","name":"Football"},"region":{"id":"1","name":"Kenya"},"competition":{"id":"10","name":"Premier League"}}]}]}`)
	events, ok := parseLiveEvents(raw)
	if !ok || len(events) != 1 {
		t.Fatal("parseLiveEvents failed")
	}
	if events[0].ID != "12345" || events[0].Name != "A - B" {
		t.Fatalf("unexpected event: %+v", events[0])
	}
}

func TestFtOdds(t *testing.T) {
	markets := []rawMarket{
		{MarketType: struct{ ID, Name string }{ID: "1234"}, Row: []rawRow{}}, // wrong type
		{MarketType: struct{ ID, Name string }{ID: "3743"}, Row: []rawRow{
			{Prices: []rawPrice{
				{Name: "1", Odds: 1.5},
				{Name: "X", Odds: 4.0},
				{Name: "2", Odds: 5.5},
			}},
		}},
	}
	odds, ok := ftOdds(markets)
	if !ok {
		t.Fatal("ftOdds should succeed")
	}
	if odds.Home != 1.5 || odds.Draw != 4.0 || odds.Away != 5.5 {
		t.Fatalf("odds mismatch: %+v", odds)
	}
}

func TestFtOddsMissing(t *testing.T) {
	_, ok := ftOdds([]rawMarket{})
	if ok {
		t.Fatal("empty markets should fail")
	}
}

func TestScoreParticipants(t *testing.T) {
	home := participantPeriodResult{
		Participant: struct{ ID, Type string }{Type: "HOME"},
		PeriodResults: []periodResult{
			{Period: periodRef{Slug: "FULL_TIME_EXCLUDING_OVERTIME"}, Result: "2", Type: "SCORE"},
			{Period: periodRef{Slug: "FIRST_HALF"}, Result: "1", Type: "SCORE"},
		},
	}
	away := participantPeriodResult{
		Participant: struct{ ID, Type string }{Type: "AWAY"},
		PeriodResults: []periodResult{
			{Period: periodRef{Slug: "FULL_TIME_EXCLUDING_OVERTIME"}, Result: "1", Type: "SCORE"},
			{Period: periodRef{Slug: "FIRST_HALF"}, Result: "0", Type: "SCORE"},
		},
	}
	h, a := scoreParticipants([]participantPeriodResult{home, away})
	if h != 2 || a != 1 {
		t.Fatalf("scores wrong: %d-%d", h, a)
	}
}

func TestParseClock(t *testing.T) {
	tests := []struct{
		in   rawDisplay
		want string
	}{
		{rawDisplay{Minute: "33", CurrentPeriod: rawPeriod{Slug: "FIRST_HALF"}}, "33'"},
		{rawDisplay{Minute: "72", CurrentPeriod: rawPeriod{Slug: "SECOND_HALF"}}, "72'"},
		{rawDisplay{Minute: "0", CurrentPeriod: rawPeriod{Slug: "FIRST_HALF"}}, "0'"},
		{rawDisplay{Minute: "", CurrentPeriod: rawPeriod{Slug: "HALF_TIME_FULL_TIME"}}, "HT"},
		{rawDisplay{Minute: "45", CurrentPeriod: rawPeriod{Slug: "HALF_TIME"}}, "HT"},
		{rawDisplay{Minute: "92", CurrentPeriod: rawPeriod{Slug: "FULL_TIME_EXCLUDING_OVERTIME"}}, "FULLTIME"},
		{rawDisplay{Minute: "", CurrentPeriod: rawPeriod{Slug: "FINISHED"}}, "FULLTIME"},
		{rawDisplay{Minute: "", CurrentPeriod: rawPeriod{Slug: ""}}, "LIVE"},
	}
	for _, tt := range tests {
		got := parseClock(tt.in)
		if got != tt.want {
			t.Errorf("parseClock(%+v) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestIsFullTimePeriod(t *testing.T) {
	if !isFullTimePeriod(rawDisplay{CurrentPeriod: rawPeriod{Slug: "FULL_TIME_EXCLUDING_OVERTIME"}}) {
		t.Fatal("FULL_TIME slug should be FT")
	}
	if isFullTimePeriod(rawDisplay{CurrentPeriod: rawPeriod{Slug: "FIRST_HALF"}}) {
		t.Fatal("FIRST_HALF slug should not be FT")
	}
}

func TestFetchLiveJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-pawa-brand") == "" {
			t.Errorf("missing x-pawa-brand header")
		}
		w.Write([]byte(`{"responses":[{"responses":[{"id":"1","name":"A-B","startTime":"2026-09-15T12:00:00Z","results":{"display":{"minute":"10","currentPeriod":{"slug":"FIRST_HALF"}},"participantPeriodResults":[{"participant":{"type":"HOME"},"periodResults":[{"period":{"slug":"FULL_TIME_EXCLUDING_OVERTIME"},"result":"0","type":"SCORE"}]},{"participant":{"type":"AWAY"},"periodResults":[{"period":{"slug":"FULL_TIME_EXCLUDING_OVERTIME"},"result":"0","type":"SCORE"}]}]},"participants":[{"name":"A","position":1},{"name":"B","position":2}],"markets":[{"marketType":{"id":"3743"},"row":[{"prices":[{"name":"1","odds":2.0},{"name":"X","odds":3.0},{"name":"2","odds":4.0}]}]}],"category":{"id":"2","name":"Football"},"region":{"id":"1","name":"Kenya"},"competition":{"id":"1","name":"Prem"}}]}]}`))
	}))
	defer srv.Close()
	p := New(Config{BaseURL: srv.URL, Interval: time.Second, Take: 5})
	events, err := p.fetchLive(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].ID != "1" {
		t.Fatalf("unexpected: %+v", events)
	}
}

func TestFetchLiveRejectsNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }))
	defer srv.Close()
	p := New(Config{BaseURL: srv.URL})
	_, err := p.fetchLive(context.Background())
	if err == nil {
		t.Fatal("expected error on 500")
	}
}

func TestRunSettlesDroppedMatch(t *testing.T) {
	// Server returns one match; on second tick returns empty → match dropped.
	// The poller must NOT fabricate a FULLTIME frame from a frozen live frame;
	// only a feed-confirmed FINISHED/FULL_TIME period gets one.
	call := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call++
		if call <= 1 {
			w.Write([]byte(`{"responses":[{"responses":[{"id":"500","name":"X-Y","startTime":"2026-09-15T12:00:00Z","results":{"display":{"minute":"90","currentPeriod":{"slug":"SECOND_HALF"}},"participantPeriodResults":[{"participant":{"type":"HOME"},"periodResults":[{"period":{"slug":"FULL_TIME_EXCLUDING_OVERTIME"},"result":"1","type":"SCORE"}]},{"participant":{"type":"AWAY"},"periodResults":[{"period":{"slug":"FULL_TIME_EXCLUDING_OVERTIME"},"result":"0","type":"SCORE"}]}]},"participants":[{"name":"X","position":1},{"name":"Y","position":2}],"markets":[{"marketType":{"id":"3743"},"row":[{"prices":[{"name":"1","odds":5.0},{"name":"X","odds":3.0},{"name":"2","odds":8.0}]}]}],"category":{"id":"2","name":"Football"},"region":{"id":"1","name":"K"},"competition":{"id":"1","name":"P"}}]}]}`))
		} else {
			w.Write([]byte(`{"responses":[{"responses":[]}]}`))
		}
	}))
	defer srv.Close()

	p := New(Config{BaseURL: srv.URL, Interval: 50 * time.Millisecond, Take: 5})
	var mu sync.Mutex
	var frames []stream.Match
	publish := func(_ context.Context, m stream.Match) error {
		mu.Lock()
		defer mu.Unlock()
		frames = append(frames, m)
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 800*time.Millisecond)
	defer cancel()
	_ = p.Run(ctx, publish)

	mu.Lock()
	defer mu.Unlock()
	for _, f := range frames {
		if f.MatchID == "bp-500" && f.Clock == "FULLTIME" {
			t.Fatalf("dropped match must not emit fabricated FULLTIME, got frame %+v", f)
		}
	}
}

func TestRunConfirmedFullTimeEmitsOnce(t *testing.T) {
	// A feed that explicitly confirms FULL_TIME must emit exactly one FULLTIME
	// frame even if repeated on later ticks (ftEmitted guard).
	call := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call++
		w.Write([]byte(`{"responses":[{"responses":[{"id":"501","name":"X-Y","startTime":"2026-09-15T12:00:00Z","results":{"display":{"minute":"","currentPeriod":{"slug":"FULL_TIME_EXCLUDING_OVERTIME"}},"participantPeriodResults":[{"participant":{"type":"HOME"},"periodResults":[{"period":{"slug":"FULL_TIME_EXCLUDING_OVERTIME"},"result":"2","type":"SCORE"}]},{"participant":{"type":"AWAY"},"periodResults":[{"period":{"slug":"FULL_TIME_EXCLUDING_OVERTIME"},"result":"1","type":"SCORE"}]}]},"participants":[{"name":"X","position":1},{"name":"Y","position":2}],"markets":[{"marketType":{"id":"3743"},"row":[{"prices":[{"name":"1","odds":2.0},{"name":"X","odds":3.0},{"name":"2","odds":8.0}]}]}],"category":{"id":"2","name":"Football"},"region":{"id":"1","name":"K"},"competition":{"id":"1","name":"P"}}]}]}`))
	}))
	defer srv.Close()

	p := New(Config{BaseURL: srv.URL, Interval: 40 * time.Millisecond, Take: 5})
	var mu sync.Mutex
	var frames []stream.Match
	publish := func(_ context.Context, m stream.Match) error {
		mu.Lock()
		defer mu.Unlock()
		frames = append(frames, m)
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	_ = p.Run(ctx, publish)

	mu.Lock()
	defer mu.Unlock()
	seen := 0
	for _, f := range frames {
		if f.MatchID != "bp-501" {
			continue
		}
		if f.Clock != "FULLTIME" {
			t.Fatalf("feed-confirmed FT frame must carry Clock=FULLTIME, got %+v", f)
		}
		if f.Score.Home != 2 || f.Score.Away != 1 {
			t.Fatalf("FT score = %d-%d, want 2-1", f.Score.Home, f.Score.Away)
		}
		seen++
	}
	if seen == 0 {
		t.Fatal("expected feed-confirmed FULLTIME frames for bp-501")
	}
}
