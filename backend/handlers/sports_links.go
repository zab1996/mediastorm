package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/gorilla/mux"

	"novastream/internal/datastore"
	"novastream/models"
)

// SportsLinksHandler serves the "Manage Team Channels" feature: CRUD over which Live TV
// channels are permanently linked (primary + backups) to which team, plus the batched
// lookup GameCard uses to show a quick-play button for teams that already have a link.
// Global/shared state, not per-user - see the sports feature plan for why.
type SportsLinksHandler struct {
	links       datastore.SportsLinksRepository
	liveHandler *LiveHandler
}

// NewSportsLinksHandler creates a new sports links handler.
func NewSportsLinksHandler(links datastore.SportsLinksRepository, liveHandler *LiveHandler) *SportsLinksHandler {
	return &SportsLinksHandler{links: links, liveHandler: liveHandler}
}

// channelLinkRequest is the request body for setting a primary or adding a backup link.
type channelLinkRequest struct {
	ChannelTvgID string `json:"channelTvgId"`
	ChannelName  string `json:"channelName"`
	ChannelURL   string `json:"channelUrl"`
	ChannelLogo  string `json:"channelLogo"`
	SourceID     string `json:"sourceId"`
	SourceName   string `json:"sourceName"`
}

func (req channelLinkRequest) valid() bool {
	return strings.TrimSpace(req.ChannelName) != "" && strings.TrimSpace(req.ChannelURL) != ""
}

// resolveChannelSet builds lookup sets (by normalized tvg-id and by normalized channel
// name) from the caller's current live channel list, so links can be marked
// Resolved/unresolved against what's actually in the playlist right now - see migration
// 056's comment on why links are a denormalized snapshot rather than a bare channel ID.
func resolveChannelSet(channels []LiveChannel) (byTvgID map[string]struct{}, byName map[string]struct{}) {
	byTvgID = make(map[string]struct{}, len(channels))
	byName = make(map[string]struct{}, len(channels))
	for _, ch := range channels {
		if tvgID := normalizeForMatch(ch.TvgID); tvgID != "" {
			byTvgID[tvgID] = struct{}{}
		}
		if name := normalizeForMatch(ch.Name); name != "" {
			byName[name] = struct{}{}
		}
	}
	return byTvgID, byName
}

func markResolved(link *models.SportsTeamChannelLink, byTvgID, byName map[string]struct{}) {
	if tvgID := normalizeForMatch(link.ChannelTvgID); tvgID != "" {
		if _, ok := byTvgID[tvgID]; ok {
			link.Resolved = true
			return
		}
	}
	if name := normalizeForMatch(link.ChannelName); name != "" {
		if _, ok := byName[name]; ok {
			link.Resolved = true
			return
		}
	}
	link.Resolved = false
}

// GetTeams lists all teams tracked for a league (?league=mlb, required) with their current
// links, resolved against the caller's live channel list.
func (h *SportsLinksHandler) GetTeams(w http.ResponseWriter, r *http.Request) {
	league := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("league")))
	if league == "" {
		http.Error(w, `{"error":"missing league"}`, http.StatusBadRequest)
		return
	}

	teams, err := h.links.ListTeamsByLeague(r.Context(), league)
	if err != nil {
		log.Printf("[sports-links] GetTeams: list teams failed: %v", err)
		http.Error(w, `{"error":"failed to list teams"}`, http.StatusInternalServerError)
		return
	}

	teamIDs := make([]string, len(teams))
	for i, t := range teams {
		teamIDs[i] = t.ID
	}
	linksByTeam, err := h.links.GetLinksForTeams(r.Context(), teamIDs)
	if err != nil {
		log.Printf("[sports-links] GetTeams: get links failed: %v", err)
		http.Error(w, `{"error":"failed to load channel links"}`, http.StatusInternalServerError)
		return
	}

	byTvgID, byName := h.currentChannelSet(r)

	result := make([]models.SportsTeamWithLinks, 0, len(teams))
	for _, t := range teams {
		links := linksByTeam[t.ID]
		for i := range links {
			markResolved(&links[i], byTvgID, byName)
		}
		result = append(result, models.SportsTeamWithLinks{SportsTeamRecord: t, Links: links})
	}
	writeSportsJSON(w, map[string]any{"teams": result})
}

// currentChannelSet fetches the caller's live channel list once, tolerating failure (an
// unreachable playlist source shouldn't block viewing/managing links - it just means
// nothing can be marked Resolved this request).
func (h *SportsLinksHandler) currentChannelSet(r *http.Request) (byTvgID, byName map[string]struct{}) {
	if h.liveHandler == nil {
		return map[string]struct{}{}, map[string]struct{}{}
	}
	channels, err := h.liveHandler.FetchFilteredChannelsForRequest(r)
	if err != nil {
		log.Printf("[sports-links] failed to fetch live channels for resolution: %v", err)
		return map[string]struct{}{}, map[string]struct{}{}
	}
	return resolveChannelSet(channels)
}

// GetTeamLinks returns one team's current channel links.
func (h *SportsLinksHandler) GetTeamLinks(w http.ResponseWriter, r *http.Request) {
	teamID := strings.TrimSpace(mux.Vars(r)["teamId"])
	if teamID == "" {
		http.Error(w, `{"error":"missing team id"}`, http.StatusBadRequest)
		return
	}
	links, err := h.links.GetLinksForTeam(r.Context(), teamID)
	if err != nil {
		log.Printf("[sports-links] GetTeamLinks failed: %v", err)
		http.Error(w, `{"error":"failed to load channel links"}`, http.StatusInternalServerError)
		return
	}
	byTvgID, byName := h.currentChannelSet(r)
	for i := range links {
		markResolved(&links[i], byTvgID, byName)
	}
	writeSportsJSON(w, map[string]any{"links": links})
}

// SetPrimaryLink sets (or replaces) a team's primary channel.
func (h *SportsLinksHandler) SetPrimaryLink(w http.ResponseWriter, r *http.Request) {
	teamID := strings.TrimSpace(mux.Vars(r)["teamId"])
	if teamID == "" {
		http.Error(w, `{"error":"missing team id"}`, http.StatusBadRequest)
		return
	}
	var req channelLinkRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || !req.valid() {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}
	team, err := h.links.GetTeam(r.Context(), teamID)
	if err != nil {
		log.Printf("[sports-links] SetPrimaryLink: get team failed: %v", err)
		http.Error(w, `{"error":"failed to look up team"}`, http.StatusInternalServerError)
		return
	}
	if team == nil {
		http.Error(w, `{"error":"team not found"}`, http.StatusNotFound)
		return
	}
	saved, err := h.links.SetPrimaryLink(r.Context(), teamID, models.SportsTeamChannelLink{
		ChannelTvgID: req.ChannelTvgID,
		ChannelName:  req.ChannelName,
		ChannelURL:   req.ChannelURL,
		ChannelLogo:  req.ChannelLogo,
		SourceID:     req.SourceID,
		SourceName:   req.SourceName,
	})
	if err != nil {
		log.Printf("[sports-links] SetPrimaryLink failed: %v", err)
		http.Error(w, `{"error":"failed to save primary channel"}`, http.StatusInternalServerError)
		return
	}
	saved.Resolved = true // just set by the caller from their own live channel list
	writeSportsJSON(w, saved)
}

// AddBackupLink appends a backup channel to a team.
func (h *SportsLinksHandler) AddBackupLink(w http.ResponseWriter, r *http.Request) {
	teamID := strings.TrimSpace(mux.Vars(r)["teamId"])
	if teamID == "" {
		http.Error(w, `{"error":"missing team id"}`, http.StatusBadRequest)
		return
	}
	var req channelLinkRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || !req.valid() {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}
	team, err := h.links.GetTeam(r.Context(), teamID)
	if err != nil {
		log.Printf("[sports-links] AddBackupLink: get team failed: %v", err)
		http.Error(w, `{"error":"failed to look up team"}`, http.StatusInternalServerError)
		return
	}
	if team == nil {
		http.Error(w, `{"error":"team not found"}`, http.StatusNotFound)
		return
	}
	saved, err := h.links.AddBackupLink(r.Context(), teamID, models.SportsTeamChannelLink{
		ChannelTvgID: req.ChannelTvgID,
		ChannelName:  req.ChannelName,
		ChannelURL:   req.ChannelURL,
		ChannelLogo:  req.ChannelLogo,
		SourceID:     req.SourceID,
		SourceName:   req.SourceName,
	})
	if err != nil {
		log.Printf("[sports-links] AddBackupLink failed: %v", err)
		http.Error(w, `{"error":"failed to add backup channel"}`, http.StatusInternalServerError)
		return
	}
	saved.Resolved = true
	writeSportsJSON(w, saved)
}

// DeleteLink removes a channel link (primary or backup) from a team.
func (h *SportsLinksHandler) DeleteLink(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	teamID := strings.TrimSpace(vars["teamId"])
	linkID := strings.TrimSpace(vars["linkId"])
	if teamID == "" || linkID == "" {
		http.Error(w, `{"error":"missing team or link id"}`, http.StatusBadRequest)
		return
	}
	if err := h.links.DeleteLink(r.Context(), teamID, linkID); err != nil {
		log.Printf("[sports-links] DeleteLink failed: %v", err)
		http.Error(w, `{"error":"failed to delete channel link"}`, http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type reorderBackupsRequest struct {
	LinkIDs []string `json:"linkIds"`
}

// ReorderBackups rewrites a team's backup channel order.
func (h *SportsLinksHandler) ReorderBackups(w http.ResponseWriter, r *http.Request) {
	teamID := strings.TrimSpace(mux.Vars(r)["teamId"])
	if teamID == "" {
		http.Error(w, `{"error":"missing team id"}`, http.StatusBadRequest)
		return
	}
	var req reorderBackupsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.LinkIDs) == 0 {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}
	if err := h.links.ReorderBackups(r.Context(), teamID, req.LinkIDs); err != nil {
		log.Printf("[sports-links] ReorderBackups failed: %v", err)
		http.Error(w, `{"error":"failed to reorder backup channels"}`, http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// GetLinkedChannels is the batched lookup GameCard uses: given several team IDs (a
// scoreboard page's worth of games), return each team's current links in one round trip
// instead of one request per team.
func (h *SportsLinksHandler) GetLinkedChannels(w http.ResponseWriter, r *http.Request) {
	idsParam := strings.TrimSpace(r.URL.Query().Get("ids"))
	if idsParam == "" {
		writeSportsJSON(w, map[string]any{"linksByTeam": map[string][]models.SportsTeamChannelLink{}})
		return
	}
	teamIDs := strings.Split(idsParam, ",")
	for i := range teamIDs {
		teamIDs[i] = strings.TrimSpace(teamIDs[i])
	}
	linksByTeam, err := h.links.GetLinksForTeams(r.Context(), teamIDs)
	if err != nil {
		log.Printf("[sports-links] GetLinkedChannels failed: %v", err)
		http.Error(w, `{"error":"failed to load channel links"}`, http.StatusInternalServerError)
		return
	}
	byTvgID, byName := h.currentChannelSet(r)
	for teamID, links := range linksByTeam {
		for i := range links {
			markResolved(&links[i], byTvgID, byName)
		}
		linksByTeam[teamID] = links
	}
	writeSportsJSON(w, map[string]any{"linksByTeam": linksByTeam})
}

// Options handles CORS preflight requests.
func (h *SportsLinksHandler) Options(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

// SyncTeamsFromGames upserts every team appearing in the given games into sports_teams,
// keyed on league+ESPN team ID. Called after each scoreboard refresh (see main.go) so
// "Manage Team Channels" has team identities to link against - a team only appears once
// the poller has observed it playing a game (see the plan's known coverage gap: an
// off-day/off-season team won't show up until then).
func (h *SportsLinksHandler) SyncTeamsFromGames(ctx context.Context, games []models.SportsGame) {
	seen := make(map[string]struct{})
	for _, game := range games {
		for _, team := range []models.SportsTeam{game.HomeTeam, game.AwayTeam} {
			if team.ID == "" {
				continue
			}
			id := fmt.Sprintf("%s:%s", game.League, team.ID)
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			err := h.links.UpsertTeam(ctx, models.SportsTeamRecord{
				ID:           id,
				League:       game.League,
				EspnTeamID:   team.ID,
				Name:         team.Name,
				Abbreviation: team.Abbreviation,
				LogoURL:      team.LogoURL,
			})
			if err != nil {
				log.Printf("[sports-links] SyncTeamsFromGames: upsert team %s failed: %v", id, err)
			}
		}
	}
}
