package handlers

import (
	"context"
	"encoding/json"
	"log"
	"math"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/gorilla/mux"

	"novastream/models"
	"novastream/services/sports"
)

// alphaNumOnlyRegex strips everything but letters/digits so broadcast-name matching isn't
// fooled by punctuation/spacing differences between ESPN's broadcast label and a provider's
// channel naming (e.g. ESPN's "MLB.TV" vs a channel literally named "MLB TV" or "MLBTV" -
// a plain substring match on "mlb.tv" would miss both).
var alphaNumOnlyRegex = regexp.MustCompile(`[^a-z0-9]`)

func normalizeForMatch(s string) string {
	return alphaNumOnlyRegex.ReplaceAllString(strings.ToLower(s), "")
}

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
		writeSportsJSON(w, map[string]any{"streams": []models.SportsStreamMatch{}, "groups": []models.SportsStreamGroup{}})
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
	streams, groups := buildSportsStreamGroups(matches)
	writeSportsJSON(w, map[string]any{"streams": streams, "groups": groups})
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

// matchGameToChannels scores every candidate through the same token-aware team/network
// matcher used by Manage Team Channels. Current and next EPG programs can promote a
// channel, but weak candidates remain visible as Possible Matches.
func matchGameToChannels(game models.SportsGame, channels []LiveChannel, epgService LiveEPGNowPlayingProvider, broadcastFilter string) []models.SportsStreamMatch {
	home := identityFromTeam(game.HomeTeam, game.League)
	away := identityFromTeam(game.AwayTeam, game.League)
	nowByID := make(map[string]models.EPGNowPlaying)
	if epgService != nil {
		ids := make([]string, 0, len(channels))
		for _, channel := range channels {
			if strings.TrimSpace(channel.TvgID) != "" {
				ids = append(ids, channel.TvgID)
			}
		}
		for _, now := range epgService.GetNowPlaying(ids) {
			nowByID[strings.ToLower(now.ChannelID)] = now
		}
	}

	matches := make([]models.SportsStreamMatch, 0)
	for _, channel := range channels {
		channelLifecycle := sportsLifecycle(channel.Name)
		if channelLifecycle == "" {
			channelLifecycle = sportsLifecycle(channel.TvgName)
		}
		if channelLifecycle == sportsLifecycleEnded {
			continue
		}
		var evidence sportsEvidence
		if broadcastFilter != "" {
			evidence = scoreBroadcastText(channel.Name, broadcastFilter)
			if evidence.score <= 0 && channel.TvgName != "" {
				evidence = scoreBroadcastText(channel.TvgName, broadcastFilter)
			}
			if evidence.score <= 0 {
				continue
			}
		} else {
			evidence = scoreMatchupText(channel.Name, home, away)
			if alternate := scoreMatchupText(channel.TvgName, home, away); alternate.score > evidence.score {
				evidence = alternate
			}
		}

		if now, ok := nowByID[strings.ToLower(channel.TvgID)]; ok {
			for _, program := range []*models.EPGProgram{now.Current, now.Next} {
				if program == nil {
					continue
				}
				programEvidence := scoreMatchupText(program.Title, home, away)
				if programEvidence.score <= 0 {
					continue
				}
				if !game.StartTime.IsZero() && !program.Start.IsZero() {
					distance := program.Start.Sub(game.StartTime)
					if distance < 0 {
						distance = -distance
					}
					switch {
					case distance <= 2*time.Hour:
						programEvidence.score = math.Min(1, programEvidence.score+0.04)
					case distance > 12*time.Hour:
						programEvidence.score -= 0.25
					case distance > 6*time.Hour:
						programEvidence.score -= 0.15
					default:
						programEvidence.score -= 0.06
					}
				}
				if broadcastFilter != "" {
					if programEvidence.score >= strongSportsConfidence {
						evidence.score = math.Max(evidence.score, 0.99)
						evidence.reason = broadcastFilter + " with both teams in EPG"
						evidence.terms = programEvidence.terms
						evidence.program = program.Title
						evidence.on = "epg-title"
					}
				} else if programEvidence.score > evidence.score {
					programEvidence.score = math.Min(1, programEvidence.score+0.04)
					programEvidence.reason = "Both teams in EPG"
					programEvidence.program = program.Title
					programEvidence.on = "epg-title"
					evidence = programEvidence
				}
			}
		}

		if evidence.score <= 0 {
			continue
		}
		score := roundSportsConfidence(evidence.score)
		matches = append(matches, models.SportsStreamMatch{
			ChannelID: channel.ID, ChannelName: channel.Name, ChannelURL: channel.URL,
			ChannelLogo: channel.Logo, ChannelTvgID: channel.TvgID, SourceID: channel.SourceID,
			SourceName: channel.SourceName, ProgramTitle: evidence.program,
			MatchedOn: evidence.on, Broadcast: broadcastFilter, MatchedTeam: evidence.team,
			Confidence: score, ConfidenceTier: confidenceTier(score), MatchReason: evidence.reason,
			MatchedTerms: evidence.terms, LifecycleState: channelLifecycle,
		})
	}
	sortSportsStreamMatches(matches)
	return matches
}
