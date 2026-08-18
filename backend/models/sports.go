package models

import "time"

// SportsTeam represents one side in a scheduled or in-progress game.
type SportsTeam struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Abbreviation string `json:"abbreviation,omitempty"`
	LogoURL      string `json:"logoUrl,omitempty"`
	Score        string `json:"score,omitempty"`
	Winner       bool   `json:"winner,omitempty"`
}

// SportsGameStatus is the coarse lifecycle state of a game.
type SportsGameStatus string

const (
	SportsGameScheduled SportsGameStatus = "scheduled"
	SportsGameLive      SportsGameStatus = "live"
	SportsGameFinal     SportsGameStatus = "final"
)

// SportsGame represents a single scheduled, live, or completed game.
type SportsGame struct {
	ID           string           `json:"id"`
	League       string           `json:"league"` // e.g. "mlb", "nfl", "nba", "nhl"
	Sport        string           `json:"sport"`  // e.g. "baseball"
	StartTime    time.Time        `json:"startTime"`
	Status       SportsGameStatus `json:"status"`
	StatusDetail string           `json:"statusDetail,omitempty"` // e.g. "9th - 0:00", "FINAL", "7:05 PM"
	Period       string           `json:"period,omitempty"`
	Clock        string           `json:"clock,omitempty"`
	HomeTeam     SportsTeam       `json:"homeTeam"`
	AwayTeam     SportsTeam       `json:"awayTeam"`
	Broadcasts   []string         `json:"broadcasts,omitempty"` // e.g. ["ESPN", "MLB.TV"]
	VenueName    string           `json:"venue,omitempty"`
}

// SportsLeague identifies a supported league/competition.
type SportsLeague struct {
	ID    string `json:"id"`    // e.g. "mlb"
	Name  string `json:"name"`  // e.g. "MLB"
	Sport string `json:"sport"` // e.g. "baseball"
}

// SportsStreamMatch is one candidate Live TV channel/stream for watching a given game,
// found by heuristically matching the game's broadcasts/teams against the user's
// configured Live TV channels and EPG now-playing data. Best-effort, not authoritative.
type SportsStreamMatch struct {
	ChannelID    string `json:"channelId"`
	ChannelName  string `json:"channelName"`
	ChannelURL   string `json:"channelUrl"`
	ChannelLogo  string `json:"channelLogo,omitempty"`
	ChannelTvgID string `json:"channelTvgId,omitempty"`
	SourceID     string `json:"sourceId,omitempty"`
	SourceName   string `json:"sourceName,omitempty"`
	ProgramTitle string `json:"programTitle,omitempty"`
	MatchedOn    string `json:"matchedOn"` // "broadcast" | "epg-title"
}

// SportsStatus reports the health of the sports scoreboard service.
type SportsStatus struct {
	Enabled     bool       `json:"enabled"`
	LastRefresh *time.Time `json:"lastRefresh,omitempty"`
	LastError   string     `json:"lastError,omitempty"`
	GameCount   int        `json:"gameCount"`
	Refreshing  bool       `json:"refreshing"`
}
