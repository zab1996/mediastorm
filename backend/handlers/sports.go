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

// GetGameStreams heuristically matches a game to candidate channels/streams from the
// caller's configured Live TV sources - "List Streams Here" support. Best-effort: matches
// on broadcast name vs channel name, then falls back to EPG now-playing titles containing
// both team names. Not authoritative; results are ranked by confidence, not guaranteed correct.
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

	matches := matchGameToChannels(game, channels, h.epgService)
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
// Two matching strategies, ranked by confidence:
//  1. broadcast: channel name contains one of the game's broadcast names (e.g. "ESPN", "MLB.TV").
//  2. epg-title: channel's current/next EPG program title contains both team names.
func matchGameToChannels(game models.SportsGame, channels []LiveChannel, epgService LiveEPGNowPlayingProvider) []models.SportsStreamMatch {
	var matches []models.SportsStreamMatch
	seen := make(map[string]struct{})

	broadcastNeedles := make([]string, 0, len(game.Broadcasts))
	for _, b := range game.Broadcasts {
		if b = strings.TrimSpace(b); b != "" {
			broadcastNeedles = append(broadcastNeedles, strings.ToLower(b))
		}
	}

	for _, ch := range channels {
		lowerName := strings.ToLower(ch.Name)
		for _, needle := range broadcastNeedles {
			if strings.Contains(lowerName, needle) {
				matches = append(matches, models.SportsStreamMatch{
					ChannelID:    ch.ID,
					ChannelName:  ch.Name,
					ChannelURL:   ch.URL,
					ChannelLogo:  ch.Logo,
					ChannelTvgID: ch.TvgID,
					SourceID:     ch.SourceID,
					SourceName:   ch.SourceName,
					MatchedOn:    "broadcast",
				})
				seen[ch.ID] = struct{}{}
				break
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
			if len(channelIDs) > 0 {
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

	if matches == nil {
		matches = []models.SportsStreamMatch{}
	}
	return matches
}
