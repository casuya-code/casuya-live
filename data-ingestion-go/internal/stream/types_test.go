package stream

import (
	"testing"
	"time"
)

func TestValidateRejectsEmptyMatchID(t *testing.T) {
	m := Match{MatchID: "  ", Markets: map[string]Market{"1H_1X2": {}}}
	if err := m.Validate(); err != ErrEmptyMatchID {
		t.Fatalf("expected ErrEmptyMatchID, got %v", err)
	}
}

func TestValidateRejectsNoMarkets(t *testing.T) {
	m := Match{MatchID: "m1", Markets: map[string]Market{}}
	if err := m.Validate(); err != ErrBadMarket {
		t.Fatalf("expected ErrBadMarket, got %v", err)
	}
}

func TestValidateRejectsNonPositiveOdds(t *testing.T) {
	m := Match{
		MatchID: "m1",
		Markets: map[string]Market{"1H_1X2": {Odds: OddsSet{Home: 2.1, Draw: 0, Away: 3.4}}},
	}
	if err := m.Validate(); err != ErrBadOdds {
		t.Fatalf("expected ErrBadOdds, got %v", err)
	}
}

func TestValidateAcceptsValidMatch(t *testing.T) {
	m := Match{
		MatchID: "m1",
		Markets: map[string]Market{"1H_1X2": {Odds: OddsSet{Home: 2.1, Draw: 3.4, Away: 5.25}}},
	}
	if err := m.Validate(); err != nil {
		t.Fatalf("expected valid match, got %v", err)
	}
}

func TestValidateCarriesMomentumStats(t *testing.T) {
	m := Match{
		MatchID: "m1",
		Markets: map[string]Market{"2H_1X2": {Odds: OddsSet{Home: 2.0, Draw: 3.5, Away: 4.0}}},
		Shots: 12, ShotsOnTarget: 5, DangerousAttacks: 44, PossessionHome: 0.52,
	}
	if err := m.Validate(); err != nil {
		t.Fatalf("expected valid match, got %v", err)
	}
	if m.ShotsOnTarget > m.Shots {
		t.Fatalf("shots on target %d cannot exceed total shots %d", m.ShotsOnTarget, m.Shots)
	}
}

func TestFilterScopeDropsOutOfScopeMarkets(t *testing.T) {
	m := Match{
		MatchID: "m1",
		Markets: map[string]Market{
			"1H_1X2": {Odds: OddsSet{Home: 2.0, Draw: 3.5, Away: 4.0}},
			"3H_1X2": {Odds: OddsSet{Home: 3.0, Draw: 3.2, Away: 2.1}},
		},
	}
	scope := MarketScope{Market1H1X2: true}
	m.FilterScope(scope)
	if _, ok := m.Markets[Market2H1X2]; ok {
		t.Fatalf("out-of-scope market survived filtering")
	}
	if len(m.Markets) != 1 {
		t.Fatalf("expected exactly one market, got %d", len(m.Markets))
	}
}

func TestParseScopeEmptyFallsBackToDefault(t *testing.T) {
	scope := parseScope("")
	if !scope[Market2H1X2] {
		t.Fatalf("default scope must include 2H_1X2")
	}
}

func TestMatchFrameCarriesMomentumStats(t *testing.T) {
	f := matchFrame{
		MatchID: "m1",
		Kickoff: 1786600000,
		Clock:   "31",
		Markets: map[string]Market{"1H_1X2": {}},
		Shots:   9, ShotsOnTarget: 4, DangerousAttacks: 27, PossessionHome: 0.58,
	}
	m := f.toMatch()
	if m.Shots != 9 || m.ShotsOnTarget != 4 || m.DangerousAttacks != 27 || m.PossessionHome != 0.58 {
		t.Fatalf("momentum stats not carried into Match: %+v", m)
	}
	if m.Kickoff.Equal(time.Unix(1786600000, 0)) == false {
		t.Fatalf("kickoff not parsed as unix seconds")
	}
}