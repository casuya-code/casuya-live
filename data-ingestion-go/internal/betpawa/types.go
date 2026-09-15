package betpawa

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/casuya-live/data-ingestion/internal/stream"
)

// --- raw JSON types (betPawa pawablox sportsbook JSON via Accept: application/json) ---

type rootResponse struct {
	Responses []queryResponse `json:"responses"`
}

type queryResponse struct {
	Responses []rawEvent `json:"responses"`
}

type rawEvent struct {
	ID               string                 `json:"id"`
	Name             string                 `json:"name"`
	StartTime        string                 `json:"startTime"`
	TotalMarketCount string                 `json:"totalMarketCount"`
	Category         struct{ ID, Name string } `json:"category"`
	Region           struct{ ID, Name string } `json:"region"`
	Competition      struct{ ID, Name string } `json:"competition"`
	Results          rawResults             `json:"results"`
	Participants     []rawParticipant       `json:"participants"`
	Markets          []rawMarket            `json:"markets"`
}

type rawParticipant struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Position int    `json:"position"` // 1=home, 2=away
}

type rawResults struct {
	Display rawDisplay `json:"display"`
	ParticipantPeriodResults []participantPeriodResult `json:"participantPeriodResults"`
}

type rawDisplay struct {
	Minute        string     `json:"minute"`
	CurrentPeriod rawPeriod  `json:"currentPeriod"`
}

type rawPeriod struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
}

type participantPeriodResult struct {
	Participant    struct{ ID, Type string }  `json:"participant"`
	PeriodResults  []periodResult             `json:"periodResults"`
}

type periodResult struct {
	Period periodRef   `json:"period"`
	Result string      `json:"result"`
	Type   string      `json:"type"`
}

type periodRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
}

type rawMarket struct {
	MarketType struct{ ID, Name string } `json:"marketType"`
	Row        []rawRow                  `json:"row"`
}

type rawRow struct {
	Prices []rawPrice `json:"prices"`
}

type rawPrice struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	TypeID      string  `json:"typeId"`
	Odds        float64 `json:"odds"`
	DisplayName string  `json:"displayName"`
}

// --- parsed output helpers ---

func parseLiveEvents(body []byte) ([]rawEvent, bool) {
	var r rootResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, false
	}
	if len(r.Responses) == 0 {
		return nil, false
	}
	out := make([]rawEvent, 0, len(r.Responses[0].Responses))
	for _, e := range r.Responses[0].Responses {
		out = append(out, e)
	}
	return out, true
}

func ftOdds(markets []rawMarket) (stream.OddsSet, bool) {
	for _, m := range markets {
		if m.MarketType.ID != "3743" || len(m.Row) == 0 {
			continue
		}
		var odds stream.OddsSet
		for _, p := range m.Row[0].Prices {
			switch strings.ToUpper(p.Name) {
			case "1":
				odds.Home = p.Odds
			case "X":
				odds.Draw = p.Odds
			case "2":
				odds.Away = p.Odds
			}
		}
		if odds.Home > 0 && odds.Draw > 0 && odds.Away > 0 {
			return odds, true
		}
	}
	return stream.OddsSet{}, false
}

func scoreParticipants(parts []participantPeriodResult) (home, away int) {
	for _, pp := range parts {
		for _, pr := range pp.PeriodResults {
			if pr.Period.Slug != "FULL_TIME_EXCLUDING_OVERTIME" || pr.Type != "SCORE" {
				continue
			}
			n, _ := strconv.Atoi(pr.Result)
			if strings.EqualFold(pp.Participant.Type, "HOME") {
				home = n
			} else if strings.EqualFold(pp.Participant.Type, "AWAY") {
				away = n
			}
		}
	}
	return
}

func parseClock(d rawDisplay) string {
	slug := strings.ToUpper(d.CurrentPeriod.Slug)
	if strings.Contains(slug, "HALF_TIME") || slug == "HALF" {
		return "HT"
	}
	if strings.Contains(slug, "FULL_TIME") || strings.Contains(slug, "FINISHED") || strings.Contains(slug, "ENDED") {
		return "FULLTIME"
	}
	minute := strings.TrimSpace(d.Minute)
	if n, err := strconv.Atoi(minute); err == nil && n >= 0 {
		return strconv.Itoa(n) + "'"
	}
	return "LIVE"
}

func isFullTimePeriod(d rawDisplay) bool {
	slug := strings.ToUpper(d.CurrentPeriod.Slug)
	if strings.Contains(slug, "HALF_TIME") {
		return false
	}
	return strings.Contains(slug, "FULL_TIME") || strings.Contains(slug, "FINISHED") || strings.Contains(slug, "ENDED")
}

func kickoffTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t
}
