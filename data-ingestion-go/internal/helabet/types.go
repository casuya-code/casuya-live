package helabet

import (
	"strconv"
	"strings"
)

// rawMatch maps the EveryMatrix live-feed JSON structure for one fixture.
type rawMatch struct {
	ID          int          `json:"id"`
	ZonePlay    int          `json:"zonePlay"`
	Sport       sportRef     `json:"sport"`
	Liga        champRef     `json:"liga"`
	Opponent1   teamRef      `json:"opponent1"`
	Opponent2   teamRef      `json:"opponent2"`
	Scores      *scores      `json:"scores"`
	EventGroups []eventGroup `json:"eventGroups"`
	StartTs     int64        `json:"startTs"`
}

type sportRef struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type champRef struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type teamRef struct {
	FullName string `json:"fullName"`
}

type scores struct {
	FullScore     string      `json:"fullScore"`
	ScoreOpp1     int         `json:"scoreOpp1"`
	ScoreOpp2     int         `json:"scoreOpp2"`
	StatusLineStr string      `json:"statusLineStr"`
	CurrentPeriod int         `json:"currentPeriod"`
	Timer         *matchTimer `json:"timer"`
	TabloStats    *tabloStats `json:"tabloStats"`
}

type matchTimer struct {
	TimeSec  int  `json:"timeSec"`
	TimeRun  bool `json:"timeRun"`
}

type tabloStats struct {
	Stats0 []statItem `json:"0"`
}

type statItem struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	S1   string `json:"s1"`
	S2   string `json:"s2"`
}

type eventGroup struct {
	GroupID  int      `json:"groupId"`
	Events   [][]event `json:"events"`
}

type event struct {
	Type int     `json:"type"`
	Cf   float64 `json:"cf"`
}

// ParseClock converts the vendor's status line (e.g. "6 minutes", "HT",
// "Match finished") into the pipeline clock contract ("6'", "HT", "FULLTIME").
func ParseClock(s string, timer *matchTimer) string {
	s = strings.TrimSpace(s)
	upper := strings.ToUpper(s)
	switch {
	case upper == "HT" || strings.HasPrefix(upper, "HALF TIME") || strings.HasPrefix(upper, "HALFTIME"):
		return "HT"
	case strings.Contains(upper, "FT") || strings.Contains(upper, "FINISHED") ||
		strings.Contains(upper, "ENDED") || strings.Contains(upper, "FULL TIME"):
		return "FULLTIME"
	}
	if s != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(strings.Split(s, " ")[0])); err == nil && n > 0 {
			return strconv.Itoa(n) + "'"
		}
	}
	if timer != nil && timer.TimeSec > 0 {
		return strconv.Itoa(timer.TimeSec/60) + "'"
	}
	return "LIVE"
}

// IsFullTime reports whether the frame signals an ended match.
func IsFullTime(raw *rawMatch) bool {
	if raw.Scores == nil {
		return false
	}
	upper := strings.ToUpper(strings.TrimSpace(raw.Scores.StatusLineStr))
	if strings.Contains(upper, "FINISHED") || strings.Contains(upper, "ENDED") ||
		strings.Contains(upper, "FULL TIME") || upper == "FT" {
		return true
	}
	if raw.Scores.Timer == nil {
		return false
	}
	return !raw.Scores.Timer.TimeRun &&
		raw.Scores.CurrentPeriod >= 2 &&
		raw.Scores.Timer.TimeSec >= 5400
}

// ftEligibleForSettlement reports whether a match that is about to drop off
// the live feed has plausibly completed its full-time score (>= 89:50).
func ftEligibleForSettlement(raw *rawMatch) bool {
	if raw.Scores == nil || raw.Scores.Timer == nil {
		return false
	}
	return raw.Scores.CurrentPeriod >= 2 && raw.Scores.Timer.TimeSec >= 5390
}