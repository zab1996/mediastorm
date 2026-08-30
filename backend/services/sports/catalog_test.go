package sports

import "testing"

func TestLeagueCatalogIncludesConfiguredSports(t *testing.T) {
	required := []string{"nfl", "college-football", "nba", "wnba", "mlb", "nhl", "soccer-uefa.europa.conf", "ufc", "pga", "atp", "f1", "nascar", "indycar", "rugby-180659", "rugby-league-3"}
	seen := make(map[string]League)
	for _, league := range LeagueCatalog {
		seen[league.ID] = league
	}
	for _, id := range required {
		if _, ok := seen[id]; !ok {
			t.Errorf("league catalog missing %q", id)
		}
	}
	if seen["ufc"].SupportsTeams {
		t.Error("UFC must use event-title matching, not team linking")
	}
	if !seen["nba"].SupportsTeams {
		t.Error("NBA must expose its complete team catalog")
	}
}

func TestDefaultLeaguesAreCoreFive(t *testing.T) {
	want := []string{"nfl", "nba", "mlb", "nhl", "ufc"}
	got := defaultLeagues()
	if len(got) != len(want) {
		t.Fatalf("default leagues = %d, want %d", len(got), len(want))
	}
	for i, id := range want {
		if got[i].ID != id {
			t.Fatalf("default[%d] = %q, want %q", i, got[i].ID, id)
		}
	}
}
