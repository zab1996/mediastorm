package sports

import (
	"strconv"
	"time"

	"novastream/models"
)

// Minimal subset of ESPN's public site.api.espn.com scoreboard response shape
// (https://site.api.espn.com/apis/site/v2/sports/{sport}/{league}/scoreboard).
// Undocumented/unstable third-party API - decode defensively, ignore unknown fields.

type espnScoreboardResponse struct {
	Events []espnEvent `json:"events"`
}

type espnEvent struct {
	ID           string            `json:"id"`
	Date         string            `json:"date"`
	Name         string            `json:"name"`
	ShortName    string            `json:"shortName"`
	Competitions []espnCompetition `json:"competitions"`
}

// ESPN's scoreboard timestamps are usually RFC3339 but sometimes omit seconds
// (e.g. "2026-08-30T16:15Z" instead of "2026-08-30T16:15:00Z") - try both rather
// than failing the whole decode over a single game's timestamp.
var espnDateLayouts = []string{
	time.RFC3339,
	"2006-01-02T15:04Z",
	"2006-01-02T15:04Z07:00",
}

func parseESPNDate(raw string) time.Time {
	for _, layout := range espnDateLayouts {
		if t, err := time.Parse(layout, raw); err == nil {
			return t
		}
	}
	return time.Time{}
}

type espnCompetition struct {
	Status      espnStatus       `json:"status"`
	Competitors []espnCompetitor `json:"competitors"`
	Broadcasts  []espnBroadcast  `json:"broadcasts"`
	Venue       *espnVenue       `json:"venue"`
}

type espnStatus struct {
	Clock        float64        `json:"clock"`
	DisplayClock string         `json:"displayClock"`
	Period       int            `json:"period"`
	Type         espnStatusType `json:"type"`
}

type espnStatusType struct {
	State       string `json:"state"` // "pre" | "in" | "post"
	Completed   bool   `json:"completed"`
	Description string `json:"description"`
	Detail      string `json:"detail"`
	ShortDetail string `json:"shortDetail"`
}

type espnCompetitor struct {
	HomeAway string   `json:"homeAway"` // "home" | "away"
	Score    string   `json:"score"`
	Winner   bool     `json:"winner"`
	Team     espnTeam `json:"team"`
}

type espnTeam struct {
	ID           string `json:"id"`
	DisplayName  string `json:"displayName"`
	Location     string `json:"location"`
	Name         string `json:"name"`
	Abbreviation string `json:"abbreviation"`
	Logo         string `json:"logo"`
}

type espnBroadcast struct {
	Names []string `json:"names"`
}

type espnVenue struct {
	FullName string `json:"fullName"`
}

func espnStatusToGameStatus(t espnStatusType) models.SportsGameStatus {
	switch t.State {
	case "in":
		return models.SportsGameLive
	case "post":
		return models.SportsGameFinal
	default:
		return models.SportsGameScheduled
	}
}

func espnEventToGame(event espnEvent, league League) (models.SportsGame, bool) {
	if len(event.Competitions) == 0 {
		return models.SportsGame{}, false
	}
	comp := event.Competitions[0]

	var home, away espnCompetitor
	for _, c := range comp.Competitors {
		if c.HomeAway == "home" {
			home = c
		} else if c.HomeAway == "away" {
			away = c
		}
	}

	broadcasts := make([]string, 0, len(comp.Broadcasts))
	for _, b := range comp.Broadcasts {
		broadcasts = append(broadcasts, b.Names...)
	}

	var venue string
	if comp.Venue != nil {
		venue = comp.Venue.FullName
	}

	statusDetail := comp.Status.Type.ShortDetail
	if statusDetail == "" {
		statusDetail = comp.Status.Type.Detail
	}

	game := models.SportsGame{
		ID:           event.ID,
		League:       league.ID,
		Sport:        league.Sport,
		StartTime:    parseESPNDate(event.Date),
		Status:       espnStatusToGameStatus(comp.Status.Type),
		StatusDetail: statusDetail,
		Clock:        comp.Status.DisplayClock,
		HomeTeam: models.SportsTeam{
			ID:           home.Team.ID,
			Name:         home.Team.DisplayName,
			Location:     home.Team.Location,
			Nickname:     home.Team.Name,
			Abbreviation: home.Team.Abbreviation,
			LogoURL:      home.Team.Logo,
			Score:        home.Score,
			Winner:       home.Winner,
		},
		AwayTeam: models.SportsTeam{
			ID:           away.Team.ID,
			Name:         away.Team.DisplayName,
			Location:     away.Team.Location,
			Nickname:     away.Team.Name,
			Abbreviation: away.Team.Abbreviation,
			LogoURL:      away.Team.Logo,
			Score:        away.Score,
			Winner:       away.Winner,
		},
		Broadcasts: broadcasts,
		VenueName:  venue,
	}
	if comp.Status.Period > 0 {
		game.Period = periodLabel(comp.Status.Period, league.Sport)
	}

	return game, true
}

func periodLabel(period int, sport string) string {
	switch sport {
	case "baseball":
		return ordinal(period) + " inning"
	case "basketball":
		return ordinal(period) + " quarter"
	case "football":
		return ordinal(period) + " quarter"
	case "hockey":
		return ordinal(period) + " period"
	default:
		return ordinal(period)
	}
}

func ordinal(n int) string {
	s := strconv.Itoa(n)
	if n%100 >= 11 && n%100 <= 13 {
		return s + "th"
	}
	switch n % 10 {
	case 1:
		return s + "st"
	case 2:
		return s + "nd"
	case 3:
		return s + "rd"
	default:
		return s + "th"
	}
}
