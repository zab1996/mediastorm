package sports

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"novastream/models"
)

// Minimal subset of ESPN's public site.api.espn.com scoreboard response shape
// (https://site.api.espn.com/apis/site/v2/sports/{sport}/{league}/scoreboard).
// Undocumented/unstable third-party API - decode defensively, ignore unknown fields.

type espnScoreboardResponse struct {
	Events []espnEvent `json:"events"`
}

type espnTeamsResponse struct {
	Sports []struct {
		Leagues []struct {
			Teams []struct {
				Team espnCatalogTeam `json:"team"`
			} `json:"teams"`
		} `json:"leagues"`
	} `json:"sports"`
}

type espnCatalogTeam struct {
	ID           string `json:"id"`
	DisplayName  string `json:"displayName"`
	Location     string `json:"location"`
	Name         string `json:"name"`
	Nickname     string `json:"nickname"`
	Abbreviation string `json:"abbreviation"`
	Logos        []struct {
		Href string `json:"href"`
	} `json:"logos"`
}

func espnTeamsToRecords(payload espnTeamsResponse, league League) []models.SportsTeamRecord {
	var records []models.SportsTeamRecord
	seen := make(map[string]struct{})
	for _, sport := range payload.Sports {
		for _, leaguePayload := range sport.Leagues {
			for _, entry := range leaguePayload.Teams {
				team := entry.Team
				if team.ID == "" || team.DisplayName == "" {
					continue
				}
				if _, exists := seen[team.ID]; exists {
					continue
				}
				seen[team.ID] = struct{}{}
				nickname := team.Name
				if nickname == "" {
					nickname = team.Nickname
				}
				logoURL := ""
				if len(team.Logos) > 0 {
					logoURL = team.Logos[0].Href
				}
				records = append(records, models.SportsTeamRecord{
					ID:           league.ID + ":" + team.ID,
					League:       league.ID,
					EspnTeamID:   team.ID,
					Name:         team.DisplayName,
					Location:     team.Location,
					Nickname:     nickname,
					Abbreviation: team.Abbreviation,
					LogoURL:      logoURL,
				})
			}
		}
	}
	return records
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
	HomeAway string          `json:"homeAway"` // "home" | "away"
	ID       string          `json:"id"`
	Order    int             `json:"order"`
	Score    json.RawMessage `json:"score"`
	Winner   bool            `json:"winner"`
	Team     espnTeam        `json:"team"`
	Athlete  *espnAthlete    `json:"athlete"`
}

type espnAthlete struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	FullName    string `json:"fullName"`
	ShortName   string `json:"shortName"`
	Headshot    *struct {
		Href string `json:"href"`
	} `json:"headshot"`
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

func espnScore(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var number float64
	if json.Unmarshal(raw, &number) == nil {
		return strconv.FormatFloat(number, 'f', -1, 64)
	}
	var object struct {
		DisplayValue string  `json:"displayValue"`
		Value        float64 `json:"value"`
	}
	if json.Unmarshal(raw, &object) == nil {
		if object.DisplayValue != "" {
			return object.DisplayValue
		}
		return strconv.FormatFloat(object.Value, 'f', -1, 64)
	}
	return ""
}

func competitorTeam(c espnCompetitor) models.SportsTeam {
	if c.Athlete != nil {
		name := c.Athlete.DisplayName
		if name == "" {
			name = c.Athlete.FullName
		}
		logo := ""
		if c.Athlete.Headshot != nil {
			logo = c.Athlete.Headshot.Href
		}
		return models.SportsTeam{ID: c.Athlete.ID, Name: name, Abbreviation: c.Athlete.ShortName, LogoURL: logo, Score: espnScore(c.Score), Winner: c.Winner}
	}
	return models.SportsTeam{ID: c.Team.ID, Name: c.Team.DisplayName, Location: c.Team.Location, Nickname: c.Team.Name, Abbreviation: c.Team.Abbreviation, LogoURL: c.Team.Logo, Score: espnScore(c.Score), Winner: c.Winner}
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
	if home.Team.ID == "" && home.Athlete == nil && len(comp.Competitors) > 0 {
		home = comp.Competitors[0]
	}
	if away.Team.ID == "" && away.Athlete == nil && len(comp.Competitors) > 1 {
		away = comp.Competitors[1]
	}
	homeTeam := competitorTeam(home)
	awayTeam := competitorTeam(away)
	participants := make([]models.SportsParticipant, 0, len(comp.Competitors))
	for index, candidate := range comp.Competitors {
		team := competitorTeam(candidate)
		position := candidate.Order
		if position == 0 {
			position = index + 1
		}
		participants = append(participants, models.SportsParticipant{ID: team.ID, Name: team.Name, Abbreviation: team.Abbreviation, LogoURL: team.LogoURL, Score: team.Score, Position: position, Winner: team.Winner})
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
		Title:        strings.TrimSpace(event.Name),
		EventKind:    league.EventKind,
		League:       league.ID,
		Sport:        league.Sport,
		StartTime:    parseESPNDate(event.Date),
		Status:       espnStatusToGameStatus(comp.Status.Type),
		StatusDetail: statusDetail,
		Clock:        comp.Status.DisplayClock,
		HomeTeam:     homeTeam,
		AwayTeam:     awayTeam,
		Broadcasts:   broadcasts,
		VenueName:    venue,
		Participants: participants,
	}
	if game.Title == "" {
		game.Title = fmt.Sprintf("%s vs %s", awayTeam.Name, homeTeam.Name)
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
