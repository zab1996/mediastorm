package epg

import (
	"fmt"
	"testing"
	"time"

	"novastream/models"
)

func TestGetNowPlayingUsesIndexedFallbackForLargeChannelBatch(t *testing.T) {
	now := time.Now().UTC()
	service := &Service{schedule: &models.EPGSchedule{
		Channels: map[string]models.EPGChannel{"nesn.us": {ID: "nesn.us", Name: "NESN"}},
		Programs: map[string][]models.EPGProgram{
			"nesn.us": {{ChannelID: "nesn.us", Title: "Red Sox at Yankees", Start: now.Add(-time.Hour), Stop: now.Add(time.Hour)}},
		},
	}}

	ids := make([]string, 0, 2501)
	for i := 0; i < 2500; i++ {
		ids = append(ids, fmt.Sprintf("unmatched-%d", i))
	}
	ids = append(ids, "NESN HD")

	got := service.GetNowPlaying(ids)
	if len(got) != len(ids) {
		t.Fatalf("GetNowPlaying returned %d rows, want %d", len(got), len(ids))
	}
	last := got[len(got)-1]
	if last.Current == nil || last.Current.Title != "Red Sox at Yankees" {
		t.Fatalf("indexed normalized-name fallback = %+v", last)
	}
}
