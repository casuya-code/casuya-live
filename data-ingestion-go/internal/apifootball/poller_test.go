package apifootball

import (
	"testing"
)

// ---------------------------------------------------------------------------
// parseLiveOdds
// ---------------------------------------------------------------------------

func TestParseLiveOdds_Happy(t *testing.T) {
	raw := []byte(`{
		"4593": [
			{"odd_1":"2.5","odd_x":"3.5","odd_2":"3.0"},
			{"odd_1":"2.4","odd_x":"3.4","odd_2":"3.1"},
			{"odd_1":"2.6","odd_x":"3.6","odd_2":"2.9"},
			{"odd_1":"2.5","odd_x":"3.5","odd_2":"3.0"}
		]
	}`)

	odds := parseLiveOdds(raw)
	m, ok := odds["4593"]
	if !ok {
		t.Fatal("expected odds for match 4593")
	}
	if m[0] != 2.5 {
		t.Errorf("home median = %f, want 2.5", m[0])
	}
	if m[1] != 3.5 {
		t.Errorf("draw median = %f, want 3.5", m[1])
	}
	if m[2] != 3.0 {
		t.Errorf("away median = %f, want 3.0", m[2])
	}
}

func TestParseLiveOdds_SkipsIncomplete(t *testing.T) {
	raw := []byte(`{
		"999": [{"odd_1":"2.0","odd_x":"","odd_2":"1.8"}]
	}`)
	odds := parseLiveOdds(raw)
	if _, ok := odds["999"]; ok {
		t.Error("expected no odds when draw leg missing")
	}
}

func TestParseLiveOdds_Empty(t *testing.T) {
	odds := parseLiveOdds([]byte(`{}`))
	if len(odds) != 0 {
		t.Errorf("expected empty odds, got %d", len(odds))
	}
}

func TestParseLiveOdds_Invalid(t *testing.T) {
	odds := parseLiveOdds([]byte(`not json`))
	if len(odds) != 0 {
		t.Errorf("expected empty on invalid JSON")
	}
}

// ---------------------------------------------------------------------------
// parseEvents + decodeEvent
// ---------------------------------------------------------------------------

func TestParseEvents_LegacyArray(t *testing.T) {
	raw := []byte(`[
		{
			"match_id": "4593",
			"match_hometeam_name": "Bristol City",
			"match_awayteam_name": "Norwich City",
			"league_name": "England Championship",
			"match_status": "1H",
			"match_time": "23'",
			"match_hometeam_score": "1",
			"match_awayteam_score": "0",
			"statistics": [
				{"type": "Ball Possession", "home": "58%", "away": "42%"},
				{"type": "Shots Total", "home": "7", "away": "4"},
				{"type": "Shots On Goal", "home": "3", "away": "1"},
				{"type": "Dangerous Attacks", "home": "15", "away": "10"}
			]
		}
	]`)
	events := parseEvents(raw)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	ev := events["4593"]
	if ev == nil {
		t.Fatal("expected event for match 4593")
	}
	if ev.Home != "Bristol City" {
		t.Errorf("home = %q", ev.Home)
	}
	if ev.League != "England Championship" {
		t.Errorf("league = %q", ev.League)
	}
	if ev.MatchTime != "23'" {
		t.Errorf("match_time = %q", ev.MatchTime)
	}
	if ev.HomeScore != 1 {
		t.Errorf("home score = %d", ev.HomeScore)
	}
	if ev.PossessionHome != 0.58 {
		t.Errorf("possession = %f, want 0.58", ev.PossessionHome)
	}
	if ev.Shots != 11 {
		t.Errorf("shots = %d, want 11", ev.Shots)
	}
	if ev.ShotsOnTarget != 4 {
		t.Errorf("shots on target = %d, want 4", ev.ShotsOnTarget)
	}
	if ev.DangerousAttacks != 25 {
		t.Errorf("dangerous attacks = %d, want 25", ev.DangerousAttacks)
	}
}

func TestParseEvents_Empty(t *testing.T) {
	events := parseEvents([]byte(`[]`))
	if len(events) != 0 {
		t.Errorf("expected 0 events, got %d", len(events))
	}
}

func TestParseEvents_ResultsWrapper(t *testing.T) {
	raw := []byte(`{
		"results": [
			{
				"match_id": "1",
				"match_hometeam_name": "Team A",
				"match_awayteam_name": "Team B",
				"league_name": "Ligue 2",
				"match_status": "FT",
				"match_time": "",
				"match_hometeam_score": "2",
				"match_awayteam_score": "1"
			}
		]
	}`)
	events := parseEvents(raw)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
}

func TestLiveEvent_InPlay(t *testing.T) {
	tests := []struct {
		status string
		want   bool
	}{
		{"1H", true},
		{"2H", true},
		{"ET", true},
		{"AET", true},
		{"FT", false},
		{"NS", false},
		{"HT", false},
	}
	for _, tt := range tests {
		ev := &liveEvent{Status: tt.status}
		if got := ev.inPlay(); got != tt.want {
			t.Errorf("inPlay(%q) = %v, want %v", tt.status, got, tt.want)
		}
	}
}

func TestLiveEvent_FinalScore(t *testing.T) {
	ev := &liveEvent{Status: "FT", HomeScore: 3, AwayScore: 2}
	s, ok := ev.finalScore()
	if !ok || s[0] != 3 || s[1] != 2 {
		t.Errorf("finalScore = %v, want [3 2]", s)
	}
	// AET also final
	ev2 := &liveEvent{Status: "AET", HomeScore: 1, AwayScore: 1}
	_, ok2 := ev2.finalScore()
	if !ok2 {
		t.Error("AET should be final")
	}
}

func TestMedian(t *testing.T) {
	tests := []struct {
		in   []float64
		want float64
	}{
		{[]float64{2.4, 2.5, 2.6}, 2.5},
		{[]float64{3.0, 4.0}, 3.5},
		{[]float64{1.5}, 1.5},
	}
	for _, tt := range tests {
		if got := median(tt.in); got != tt.want {
			t.Errorf("median(%v) = %f, want %f", tt.in, got, tt.want)
		}
	}
}

func TestPercent(t *testing.T) {
	if got := percent("58%"); got != 0.58 {
		t.Errorf("percent('58%%') = %f, want 0.58", got)
	}
	if got := percent("100%"); got != 1.0 {
		t.Errorf("percent('100%%') = %f, want 1.0", got)
	}
}

func TestNum(t *testing.T) {
	if got := num("7"); got != 7 {
		t.Errorf("num('7') = %d, want 7", got)
	}
	if got := num(""); got != 0 {
		t.Errorf("num('') = %d, want 0", got)
	}
}

func TestDecodeEvent_SkipsIncomplete(t *testing.T) {
	raw := []byte(`{"match_id":"x","match_hometeam_name":"","match_awayteam_name":"B","league_name":"L","match_status":"1H","match_time":"45'","match_hometeam_score":"0","match_awayteam_score":"0"}`)
	ev := decodeEvent(raw)
	if ev != nil {
		t.Error("expected nil for event with empty home team")
	}
}
