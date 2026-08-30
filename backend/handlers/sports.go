package handlers

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/mux"

	"novastream/models"
	"novastream/services/sports"
)

const sportsRefreshTimeout = 20 * time.Second

// EPG-title matching falls back to a per-channel scan of the whole EPG dataset for any
// channel ID it can't look up directly (services/epg findProgramsByChannelMatch), so
// feeding it every unmatched channel on a large playlist (tens of thousands of channels is
// common with Xtream sources) makes GetGameStreams hang for minutes. Cap it instead - the
// broadcast-name pass above already covers the common case, so skipping the EPG fallback
// on oversized playlists is a reasonable trade for staying responsive.
const maxEPGMatchChannels = 1500

// SportsHandler serves ESPN-backed scoreboard/schedule data and matches games to
// the caller's configured Live TV channels for "what can I watch this on" lookups.
type SportsHandler struct {
	service     *sports.Service
	liveHandler *LiveHandler
	epgService  LiveEPGNowPlayingProvider
}

// NewSportsHandler creates a new sports handler.
func NewSportsHandler(service *sports.Service, liveHandler *LiveHandler, epgService LiveEPGNowPlayingProvider) *SportsHandler {
	return &SportsHandler{service: service, liveHandler: liveHandler, epgService: epgService}
}

// GetScoreboard returns cached games, optionally filtered to one league (?league=mlb).
func (h *SportsHandler) GetScoreboard(w http.ResponseWriter, r *http.Request) {
	league := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("league")))
	games := h.service.GetScoreboard(league)
	if games == nil {
		games = []models.SportsGame{}
	}
	writeSportsJSON(w, map[string]any{"games": games})
}

// GetLeagues returns the tracked leagues.
func (h *SportsHandler) GetLeagues(w http.ResponseWriter, r *http.Request) {
	writeSportsJSON(w, map[string]any{"leagues": h.service.Leagues()})
}

// GetGame returns a single cached game by ID.
func (h *SportsHandler) GetGame(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(mux.Vars(r)["id"])
	if id == "" {
		http.Error(w, `{"error":"missing game id"}`, http.StatusBadRequest)
		return
	}
	game, ok := h.service.GetGame(id)
	if !ok {
		http.Error(w, `{"error":"game not found"}`, http.StatusNotFound)
		return
	}
	writeSportsJSON(w, game)
}

// GetStatus reports scoreboard cache health.
func (h *SportsHandler) GetStatus(w http.ResponseWriter, r *http.Request) {
	writeSportsJSON(w, h.service.GetStatus())
}

// Refresh triggers an immediate scoreboard refresh.
func (h *SportsHandler) Refresh(w http.ResponseWriter, r *http.Request) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), sportsRefreshTimeout)
		defer cancel()
		if err := h.service.Refresh(ctx); err != nil {
			log.Printf("[sports] refresh error: %v", err)
		}
	}()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	w.Write([]byte(`{"status":"refresh started"}`))
}

// GetGameStreams finds candidate channels/streams for watching a game - "List Streams
// Here" support. Two modes, chosen by the caller (a specific broadcast badge vs. the main
// "List Streams Here" action - see components/sports/GameScoreCard.tsx):
//   - ?broadcast=<name>: scoped lookup, only channels whose name contains that one
//     broadcast/network name (e.g. tapping a "Peacock" badge).
//   - no broadcast param: searches the two team names (and EPG now-playing titles)
//     separately, since matching every channel against every broadcast name by default
//     was too broad (e.g. returned unrelated VOD-style channels that merely contain
//     "Peacock" in their name). Not authoritative; results are heuristic, not guaranteed.
func (h *SportsHandler) GetGameStreams(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(mux.Vars(r)["id"])
	if id == "" {
		http.Error(w, `{"error":"missing game id"}`, http.StatusBadRequest)
		return
	}
	game, ok := h.service.GetGame(id)
	if !ok {
		http.Error(w, `{"error":"game not found"}`, http.StatusNotFound)
		return
	}

	if h.liveHandler == nil {
		writeSportsJSON(w, map[string]any{"streams": []models.SportsStreamMatch{}})
		return
	}

	channels, err := h.liveHandler.FetchFilteredChannelsForRequest(r)
	if err != nil {
		log.Printf("[sports] GetGameStreams: failed to fetch live channels: %v", err)
		http.Error(w, `{"error":"failed to fetch live channels"}`, http.StatusBadGateway)
		return
	}

	broadcastFilter := strings.TrimSpace(r.URL.Query().Get("broadcast"))
	matches := matchGameToChannels(game, channels, h.epgService, broadcastFilter)
	writeSportsJSON(w, map[string]any{"streams": matches})
}

// Options handles CORS preflight requests.
func (h *SportsHandler) Options(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func writeSportsJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("[sports] JSON encode error: %v", err)
	}
}

// matchGameToChannels finds Live TV channels plausibly carrying the given game.
//
// If broadcastFilter is non-empty (a specific broadcast badge was tapped), this does a
// single scoped pass: channel name contains that one broadcast/network name. Nothing else
// is matched - this is meant to answer "which of my channels look like this network",
// not "which of my channels are related to this game" (see below).
//
// If broadcastFilter is empty (the main "List Streams Here" action), it searches by team
// name instead of broadcast name, since matching every channel against every broadcast
// name by default was too broad - e.g. tapping "Peacock" on a game used to return every
// channel with "peacock" in its name, including unrelated VOD-style channels. Two strategies,
// ranked by confidence, each tagged with which team matched (MatchedTeam) so callers can
// group results per team:
//  1. team-name: channel name contains the team's nickname (last word of its full name,
//     e.g. "Marlins" from "Miami Marlins") or abbreviation.
//  2. epg-title: channel's current EPG program title contains both team names.
func matchGameToChannels(game models.SportsGame, channels []LiveChannel, epgService LiveEPGNowPlayingProvider, broadcastFilter string) []models.SportsStreamMatch {
	if broadcastFilter != "" {
		return matchChannelsByBroadcast(channels, broadcastFilter)
	}
	return matchChannelsByTeamName(game, channels, epgService)
}

// matchChannelsByBroadcast is the scoped, per-badge lookup: channel name contains the
// given broadcast/network name.
func matchChannelsByBroadcast(channels []LiveChannel, broadcast string) []models.SportsStreamMatch {
	needle := strings.ToLower(strings.TrimSpace(broadcast))
	matches := make([]models.SportsStreamMatch, 0)
	if needle == "" {
		return matches
	}
	for _, ch := range channels {
		if strings.Contains(strings.ToLower(ch.Name), needle) {
			matches = append(matches, models.SportsStreamMatch{
				ChannelID:    ch.ID,
				ChannelName:  ch.Name,
				ChannelURL:   ch.URL,
				ChannelLogo:  ch.Logo,
				ChannelTvgID: ch.TvgID,
				SourceID:     ch.SourceID,
				SourceName:   ch.SourceName,
				MatchedOn:    "broadcast",
				Broadcast:    broadcast,
			})
		}
	}
	return matches
}

// minTeamNeedleLength avoids matching on very short nicknames/abbreviations (e.g. a
// 2-letter abbreviation) that would produce mostly false positives against arbitrary
// channel names.
const minTeamNeedleLength = 4

// matchChannelsByTeamName is the default "List Streams Here" lookup: searches each
// team's name/nickname/abbreviation and current EPG program title separately, rather than
// matching against the game's broadcast list.
func matchChannelsByTeamName(game models.SportsGame, channels []LiveChannel, epgService LiveEPGNowPlayingProvider) []models.SportsStreamMatch {
	matches := make([]models.SportsStreamMatch, 0)
	seen := make(map[string]struct{})

	teams := []models.SportsTeam{game.HomeTeam, game.AwayTeam}
	for _, team := range teams {
		needles := teamNameNeedles(team)
		if len(needles) == 0 {
			continue
		}
		for _, ch := range channels {
			if _, alreadyMatched := seen[ch.ID]; alreadyMatched {
				continue
			}
			lowerName := strings.ToLower(ch.Name)
			for _, needle := range needles {
				if strings.Contains(lowerName, needle) {
					matches = append(matches, models.SportsStreamMatch{
						ChannelID:    ch.ID,
						ChannelName:  ch.Name,
						ChannelURL:   ch.URL,
						ChannelLogo:  ch.Logo,
						ChannelTvgID: ch.TvgID,
						SourceID:     ch.SourceID,
						SourceName:   ch.SourceName,
						MatchedOn:    "team-name",
						MatchedTeam:  team.Name,
					})
					seen[ch.ID] = struct{}{}
					break
				}
			}
		}
	}

	if epgService != nil {
		homeNeedle := strings.ToLower(strings.TrimSpace(game.HomeTeam.Name))
		awayNeedle := strings.ToLower(strings.TrimSpace(game.AwayTeam.Name))
		if homeNeedle != "" && awayNeedle != "" {
			channelIDs := make([]string, 0, len(channels))
			byTvgID := make(map[string][]LiveChannel)
			for _, ch := range channels {
				if _, alreadyMatched := seen[ch.ID]; alreadyMatched {
					continue
				}
				if strings.TrimSpace(ch.TvgID) == "" {
					continue
				}
				channelIDs = append(channelIDs, ch.TvgID)
				key := strings.ToLower(ch.TvgID)
				byTvgID[key] = append(byTvgID[key], ch)
			}
			if len(channelIDs) > 0 && len(channelIDs) <= maxEPGMatchChannels {
				for _, nowPlaying := range epgService.GetNowPlaying(channelIDs) {
					title := ""
					if nowPlaying.Current != nil {
						title = strings.ToLower(nowPlaying.Current.Title)
					}
					if title == "" || !strings.Contains(title, homeNeedle) || !strings.Contains(title, awayNeedle) {
						continue
					}
					for _, ch := range byTvgID[strings.ToLower(nowPlaying.ChannelID)] {
						if _, alreadyMatched := seen[ch.ID]; alreadyMatched {
							continue
						}
						programTitle := ""
						if nowPlaying.Current != nil {
							programTitle = nowPlaying.Current.Title
						}
						matches = append(matches, models.SportsStreamMatch{
							ChannelID:    ch.ID,
							ChannelName:  ch.Name,
							ChannelURL:   ch.URL,
							ChannelLogo:  ch.Logo,
							ChannelTvgID: ch.TvgID,
							SourceID:     ch.SourceID,
							SourceName:   ch.SourceName,
							ProgramTitle: programTitle,
							MatchedOn:    "epg-title",
						})
						seen[ch.ID] = struct{}{}
					}
				}
			}
		}
	}

	return matches
}

// teamNameNeedles derives the lowercase substrings to search channel names for: the
// team's nickname (last word of its full display name, e.g. "Marlins" from "Miami
// Marlins") and its abbreviation, each only if long enough to avoid mostly-false-positive
// matches against arbitrary channel names.
func teamNameNeedles(team models.SportsTeam) []string {
	var needles []string

	name := strings.TrimSpace(team.Name)
	if name != "" {
		words := strings.Fields(name)
		nickname := strings.ToLower(words[len(words)-1])
		if len(nickname) >= minTeamNeedleLength {
			needles = append(needles, nickname)
		}
	}

	abbr := strings.ToLower(strings.TrimSpace(team.Abbreviation))
	if len(abbr) >= minTeamNeedleLength {
		needles = append(needles, abbr)
	}

	return needles
}
