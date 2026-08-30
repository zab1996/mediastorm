package sports

import (
	"encoding/json"
	"testing"
)

func TestESPNTeamsToRecordsReturnsCompleteCatalogEntries(t *testing.T) {
	const fixture = `{
		"sports": [{"leagues": [{"teams": [
			{"team": {"id": "1", "displayName": "Atlanta Hawks", "location": "Atlanta", "name": "Hawks", "abbreviation": "ATL", "logos": [{"href": "https://example.test/atl.png"}]}},
			{"team": {"id": "2", "displayName": "Boston Celtics", "location": "Boston", "nickname": "Celtics", "abbreviation": "BOS"}},
			{"team": {"id": "", "displayName": "Invalid"}},
			{"team": {"id": "1", "displayName": "Duplicate Hawks"}}
		]}]}]
	}`

	var payload espnTeamsResponse
	if err := json.Unmarshal([]byte(fixture), &payload); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	records := espnTeamsToRecords(payload, League{ID: "nba"})
	if len(records) != 2 {
		t.Fatalf("got %d records, want 2", len(records))
	}
	if records[0].ID != "nba:1" || records[0].Name != "Atlanta Hawks" || records[0].Nickname != "Hawks" {
		t.Fatalf("unexpected first record: %+v", records[0])
	}
	if records[0].LogoURL != "https://example.test/atl.png" {
		t.Fatalf("unexpected logo URL: %q", records[0].LogoURL)
	}
	if records[1].ID != "nba:2" || records[1].Nickname != "Celtics" {
		t.Fatalf("unexpected second record: %+v", records[1])
	}
}
