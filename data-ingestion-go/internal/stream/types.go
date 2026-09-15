package stream

import (
	"errors"
	"strings"
	"time"
)

// Market identifiers scoped by CASUYA-LIVE ingestion targets.
const (
	Market1H1X2 = "1H_1X2"
	Market2H1X2 = "2H_1X2"
	MarketCS1H  = "CS_1H"
	MarketCS2H  = "CS_2H"
	// MarketFT1X2 is the real-vendor full-time 1X2 frame used by the
	// API-Football provider. Mock frames keep the legacy half/correct-score
	// markets above.
	MarketFT1X2 = "FULLTIME_1X2"
)

// TeamSide disambiguates the two competing sides.
type TeamSide string

const (
	HomeTeam TeamSide = "home"
	AwayTeam TeamSide = "away"
)

// OddsSet holds the three-way bookmaker odds for a 1X2-style market.
type OddsSet struct {
	Home float64 `json:"home"`
	Draw float64 `json:"draw"`
	Away float64 `json:"away"`
}

// MarketFrame is a single quoted market for one match.
type MarketFrame struct {
	MarketID  string  `json:"market_id"`
	Odds      OddsSet `json:"odds"`
	UpdatedAt int64   `json:"updated_at"`
}

// Match is the validated, storage-ready contract derived from a vendor frame.
type Match struct {
	MatchID  string            `json:"match_id"`
	Sport    string            `json:"sport"`
	League   string            `json:"league"`
	HomeTeam string            `json:"home_team"`
	AwayTeam string            `json:"away_team"`
	Kickoff  time.Time         `json:"kickoff"`
	Clock    string            `json:"clock"`
	Score    Score             `json:"score"`
	Markets  map[string]Market `json:"markets"`
	// Momentum statistics the analytics engine folds into its feature vector.
	// Vendors that omit them fall back to zeroes downstream.
	Shots            int       `json:"shots"`
	ShotsOnTarget    int       `json:"shots_on_target"`
	DangerousAttacks int       `json:"dangerous_attacks"`
	PossessionHome   float64   `json:"possession_home"`
	ReceivedAt       time.Time `json:"received_at"`
}

// Score tracks current match state.
type Score struct {
	Home int `json:"home"`
	Away int `json:"away"`
}

// Market is a validated, scoped market frame.
type Market MarketFrame

// MarketScope is the set of market IDs the ingestor is allowed to ingest.
type MarketScope map[string]bool

// DefaultScope matches the CASUYA-LIVE high-odds targeting set.
func DefaultScope() MarketScope {
	return MarketScope{
		Market1H1X2: true,
		Market2H1X2: true,
		MarketCS1H:  true,
		MarketCS2H:  true,
	}
}

// matchFrame is the raw inbound JSON envelope before boundary validation.
type matchFrame struct {
	MatchID          string            `json:"match_id"`
	Sport            string            `json:"sport"`
	League           string            `json:"league"`
	HomeTeam         string            `json:"home_team"`
	AwayTeam         string            `json:"away_team"`
	Kickoff          int64             `json:"kickoff"`
	Clock            string            `json:"clock"`
	Score            Score             `json:"score"`
	Markets          map[string]Market `json:"markets"`
	Shots            int               `json:"shots"`
	ShotsOnTarget    int               `json:"shots_on_target"`
	DangerousAttacks int               `json:"dangerous_attacks"`
	PossessionHome   float64           `json:"possession_home"`
}

func (f matchFrame) toMatch() Match {
	return Match{
		MatchID:          f.MatchID,
		Sport:            f.Sport,
		League:           f.League,
		HomeTeam:         f.HomeTeam,
		AwayTeam:         f.AwayTeam,
		Kickoff:          time.Unix(f.Kickoff, 0),
		Clock:            f.Clock,
		Score:            f.Score,
		Markets:          f.Markets,
		Shots:            f.Shots,
		ShotsOnTarget:    f.ShotsOnTarget,
		DangerousAttacks: f.DangerousAttacks,
		PossessionHome:   f.PossessionHome,
		ReceivedAt:       time.Now().UTC(),
	}
}

var (
	ErrEmptyMatchID = errors.New("match_id must not be empty")
	ErrBadMarket    = errors.New("market is not within the scoped high-odds set")
	ErrBadOdds      = errors.New("odds values must be strictly positive")
)

// FilterScope retains only the markets the ingestor is configured to ingest
// and mutates the match's market map in place. Markets outside the scope are
// dropped without failing the whole frame.
func (m *Match) FilterScope(scope MarketScope) {
	for id := range m.Markets {
		if !scope[id] {
			delete(m.Markets, id)
		}
	}
}

// Validate enforces the data boundary contract. Malformed frames must be
// rejected here so they never reach downstream storage.
func (m Match) Validate() error {
	if strings.TrimSpace(m.MatchID) == "" {
		return ErrEmptyMatchID
	}
	if len(m.Markets) == 0 {
		return ErrBadMarket
	}
	for _, market := range m.Markets {
		if err := market.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// Validate checks a single market contract.
func (m Market) Validate() error {
	for _, odd := range []float64{m.Odds.Home, m.Odds.Draw, m.Odds.Away} {
		if odd <= 0 {
			return ErrBadOdds
		}
	}
	return nil
}
