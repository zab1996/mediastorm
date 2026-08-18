// Package sports fetches live scores/schedules from ESPN's public scoreboard API
// and caches them in memory + on disk, mirroring the refresh/cache shape of
// services/epg. Leagues are a fixed MVP set (MLB/NFL/NBA/NHL) - no user settings yet.
package sports

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"novastream/internal/apiusage"
	"novastream/models"
)

const (
	defaultHTTPTimeout   = 15 * time.Second
	sportsCacheDir       = "sports"
	sportsCacheFile      = "scoreboard.json"
	espnScoreboardURLFmt = "https://site.api.espn.com/apis/site/v2/sports/%s/%s/scoreboard"
)

// League describes one supported league's ESPN sport/league slug pair.
type League struct {
	ID    string // e.g. "mlb"
	Name  string // e.g. "MLB"
	Sport string // ESPN "sport" path segment, e.g. "baseball"
	Slug  string // ESPN "league" path segment, e.g. "mlb"
}

// DefaultLeagues is the MVP fixed set of tracked leagues.
var DefaultLeagues = []League{
	{ID: "mlb", Name: "MLB", Sport: "baseball", Slug: "mlb"},
	{ID: "nfl", Name: "NFL", Sport: "football", Slug: "nfl"},
	{ID: "nba", Name: "NBA", Sport: "basketball", Slug: "nba"},
	{ID: "nhl", Name: "NHL", Sport: "hockey", Slug: "nhl"},
}

// Service fetches and caches ESPN scoreboard data.
type Service struct {
	storageDir string
	client     *http.Client
	leagues    []League

	mu          sync.RWMutex
	games       map[string][]models.SportsGame // league ID -> games
	lastUpdated time.Time
	refreshing  bool
	lastError   string
}

// NewService creates a new sports service and loads any cached scoreboard from disk.
func NewService(storageDir string) *Service {
	s := &Service{
		storageDir: storageDir,
		client: apiusage.TrackClient(&http.Client{
			Timeout: defaultHTTPTimeout,
		}, "Sports", "ESPN scoreboard fetch"),
		leagues: DefaultLeagues,
		games:   make(map[string][]models.SportsGame),
	}

	cacheDir := filepath.Join(storageDir, sportsCacheDir)
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		log.Printf("[sports] failed to create cache directory: %v", err)
	}

	if err := s.loadFromDisk(); err != nil {
		log.Printf("[sports] no cached scoreboard found or error loading: %v", err)
	}

	return s
}

// Leagues returns the fixed set of tracked leagues.
func (s *Service) Leagues() []models.SportsLeague {
	out := make([]models.SportsLeague, len(s.leagues))
	for i, l := range s.leagues {
		out[i] = models.SportsLeague{ID: l.ID, Name: l.Name, Sport: l.Sport}
	}
	return out
}

// GetStatus reports the current health of the scoreboard cache.
func (s *Service) GetStatus() models.SportsStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()

	count := 0
	for _, games := range s.games {
		count += len(games)
	}

	status := models.SportsStatus{
		Enabled:    true,
		GameCount:  count,
		Refreshing: s.refreshing,
		LastError:  s.lastError,
	}
	if !s.lastUpdated.IsZero() {
		t := s.lastUpdated
		status.LastRefresh = &t
	}
	return status
}

// GetScoreboard returns cached games for a league ID ("mlb", "nfl", ...), or all
// leagues combined if league is empty.
func (s *Service) GetScoreboard(league string) []models.SportsGame {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if league != "" {
		games := s.games[league]
		out := make([]models.SportsGame, len(games))
		copy(out, games)
		return out
	}

	var all []models.SportsGame
	for _, l := range s.leagues {
		all = append(all, s.games[l.ID]...)
	}
	return all
}

// GetGame returns a single cached game by ID, if present.
func (s *Service) GetGame(id string) (models.SportsGame, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, games := range s.games {
		for _, g := range games {
			if g.ID == id {
				return g, true
			}
		}
	}
	return models.SportsGame{}, false
}

// Refresh fetches the current scoreboard for every tracked league from ESPN.
func (s *Service) Refresh(ctx context.Context) error {
	s.mu.Lock()
	if s.refreshing {
		s.mu.Unlock()
		return nil
	}
	s.refreshing = true
	s.lastError = ""
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		s.refreshing = false
		s.mu.Unlock()
	}()

	nextGames := make(map[string][]models.SportsGame, len(s.leagues))
	var firstErr error
	for _, league := range s.leagues {
		games, err := s.fetchLeagueScoreboard(ctx, league)
		if err != nil {
			log.Printf("[sports] scoreboard fetch failed for %s: %v", league.ID, err)
			if firstErr == nil {
				firstErr = err
			}
			s.mu.RLock()
			nextGames[league.ID] = s.games[league.ID] // keep stale data for this league on error
			s.mu.RUnlock()
			continue
		}
		nextGames[league.ID] = games
	}

	s.mu.Lock()
	s.games = nextGames
	s.lastUpdated = time.Now()
	if firstErr != nil {
		s.lastError = firstErr.Error()
	}
	s.mu.Unlock()

	if err := s.saveToDisk(); err != nil {
		log.Printf("[sports] failed to save scoreboard cache: %v", err)
	}

	return firstErr
}

func (s *Service) fetchLeagueScoreboard(ctx context.Context, league League) ([]models.SportsGame, error) {
	endpoint := fmt.Sprintf(espnScoreboardURLFmt, league.Sport, league.Slug)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("espn scoreboard %s: status %d", league.ID, resp.StatusCode)
	}

	var payload espnScoreboardResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode espn scoreboard %s: %w", league.ID, err)
	}

	games := make([]models.SportsGame, 0, len(payload.Events))
	for _, event := range payload.Events {
		game, ok := espnEventToGame(event, league)
		if ok {
			games = append(games, game)
		}
	}
	return games, nil
}

func (s *Service) saveToDisk() error {
	s.mu.RLock()
	data, err := json.Marshal(s.games)
	s.mu.RUnlock()
	if err != nil {
		return fmt.Errorf("marshal scoreboard: %w", err)
	}

	cacheDir := filepath.Join(s.storageDir, sportsCacheDir)
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		return fmt.Errorf("create cache directory: %w", err)
	}

	cachePath := filepath.Join(cacheDir, sportsCacheFile)
	tmpPath := cachePath + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := os.Rename(tmpPath, cachePath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename temp file: %w", err)
	}
	return nil
}

func (s *Service) loadFromDisk() error {
	cachePath := filepath.Join(s.storageDir, sportsCacheDir, sportsCacheFile)
	data, err := os.ReadFile(cachePath)
	if err != nil {
		return err
	}
	var games map[string][]models.SportsGame
	if err := json.Unmarshal(data, &games); err != nil {
		return fmt.Errorf("unmarshal scoreboard: %w", err)
	}
	s.mu.Lock()
	s.games = games
	s.mu.Unlock()
	return nil
}
