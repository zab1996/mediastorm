package handlers

import (
	"testing"
	"time"

	"novastream/models"
)

func testTeamRecord(name, location, nickname, abbreviation string) models.SportsTeamRecord {
	return models.SportsTeamRecord{
		ID: "mlb:test", League: "mlb", Name: name, Location: location,
		Nickname: nickname, Abbreviation: abbreviation,
	}
}

func TestNormalizeSportsText(t *testing.T) {
	got := normalizeSportsText("  Montréal_Canadiens-vs.N.Y.  ")
	if got != "montreal canadiens vs n y" {
		t.Fatalf("normalizeSportsText() = %q", got)
	}
}

func TestWholeTokenBroadcastMatching(t *testing.T) {
	tests := []struct {
		name      string
		channel   string
		broadcast string
		wantMatch bool
		wantTier  string
	}{
		{name: "yes network", channel: "YES Network HD", broadcast: "YES", wantMatch: true, wantTier: "strong"},
		{name: "no substring", channel: "Yesterday Classics", broadcast: "YES", wantMatch: false},
		{name: "nesn", channel: "NESN", broadcast: "NESN", wantMatch: true, wantTier: "strong"},
		{name: "peacock movie stays possible", channel: "Peacock Movies VOD", broadcast: "Peacock", wantMatch: true, wantTier: "possible"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			evidence := scoreBroadcastText(tt.channel, tt.broadcast)
			if (evidence.score > 0) != tt.wantMatch {
				t.Fatalf("score = %.2f, want match=%v", evidence.score, tt.wantMatch)
			}
			if tt.wantMatch && confidenceTier(evidence.score) != tt.wantTier {
				t.Fatalf("tier = %q, want %q (score %.2f)", confidenceTier(evidence.score), tt.wantTier, evidence.score)
			}
		})
	}
}

func TestESPNBroadcastAliasCatalog(t *testing.T) {
	tests := []struct{ broadcast, channel string }{
		{"ESPN Unlmtd", "ESPN Unlimited HD"},
		{"NBC Sports CA", "NBCSCA"},
		{"NBC Sports Phil", "NBC Sports Philadelphia"},
		{"SportsNet LA", "Spectrum SportsNet LA"},
		{"CHSN", "Chicago Sports Network"},
		{"MASN", "Mid Atlantic Sports Network"},
		{"MLB.TV", "MLBTV"},
		{"Angels.TV", "AngelsTV"},
		{"CLEGuardians.TV", "Cleveland Guardians TV"},
		{"TVA", "TVA Sports"},
	}
	for _, tt := range tests {
		t.Run(tt.broadcast+"_"+tt.channel, func(t *testing.T) {
			if evidence := scoreBroadcastText(tt.channel, tt.broadcast); evidence.score <= 0 {
				t.Fatalf("%q did not match ESPN label %q", tt.channel, tt.broadcast)
			}
		})
	}
	for _, unsafe := range []struct{ channel, broadcast string }{
		{"Yesterday Classics", "YES"},
		{"Turner RSN Documentary", "Rangers Sports Network"},
		{"Sports News", "Sportsnet"},
	} {
		if evidence := scoreBroadcastText(unsafe.channel, unsafe.broadcast); evidence.score > 0 {
			t.Fatalf("unsafe substring %q matched %q at %.2f", unsafe.channel, unsafe.broadcast, evidence.score)
		}
	}
}

func TestSportsLifecycleFiltering(t *testing.T) {
	if got := sportsLifecycle("End | Phillies at Angels"); got != sportsLifecycleEnded {
		t.Fatalf("End lifecycle = %q", got)
	}
	if got := sportsLifecycle("Off Air: Phillies at Angels"); got != sportsLifecycleEnded {
		t.Fatalf("Off Air lifecycle = %q", got)
	}
	if got := sportsLifecycle("Live | Phillies at Angels"); got != sportsLifecycleLive {
		t.Fatalf("Live lifecycle = %q", got)
	}
	if got := sportsLifecycle("Weekend Baseball"); got != "" {
		t.Fatalf("Weekend incorrectly marked %q", got)
	}
	if evidence := scoreBroadcastText("End | Peacock 001", "Peacock"); evidence.score != 0 {
		t.Fatalf("ended Peacock score = %.2f", evidence.score)
	}
}

func TestTeamChannelSuggestionScores(t *testing.T) {
	team := testTeamRecord("Washington Nationals", "Washington", "Nationals", "WSH")
	channels := []LiveChannel{
		{ID: "dedicated", Name: "MLB Washington Nationals HD", URL: "https://example/dedicated"},
		{ID: "matchup", Name: "Mets vs Nationals", URL: "https://example/matchup"},
		{ID: "other-team", Name: "Washington Capitals", URL: "https://example/capitals"},
	}
	got := rankTeamChannelCandidates(team, channels, "", 100)
	if len(got) != 2 {
		t.Fatalf("got %d candidates, want 2: %+v", len(got), got)
	}
	if got[0].ChannelID != "dedicated" || got[0].ConfidenceTier != "strong" {
		t.Fatalf("first candidate = %+v, want strong dedicated channel", got[0])
	}
	if got[1].ChannelID != "matchup" || got[1].ConfidenceTier != "possible" || got[1].Confidence >= 0.60 {
		t.Fatalf("second candidate = %+v, want sub-0.60 possible matchup", got[1])
	}
}

func TestOnlyStrongSportsMatches(t *testing.T) {
	matches := []models.SportsStreamMatch{
		{ChannelID: "strong", Confidence: 0.85, ConfidenceTier: "strong"},
		{ChannelID: "possible", Confidence: 0.84, ConfidenceTier: "possible"},
	}
	got := onlyStrongSportsMatches(matches)
	if len(got) != 1 || got[0].ChannelID != "strong" {
		t.Fatalf("strong matches = %#v", got)
	}
}

func TestManualSearchKeepsZeroConfidenceResult(t *testing.T) {
	team := testTeamRecord("Washington Nationals", "Washington", "Nationals", "WSH")
	channels := []LiveChannel{{ID: "manual", Name: "My Custom Baseball Feed", URL: "https://example/manual"}}
	got := rankTeamChannelCandidates(team, channels, "custom", 100)
	if len(got) != 1 || got[0].Confidence != 0 || got[0].ConfidenceTier != "possible" {
		t.Fatalf("manual result = %+v, want visible 0%% possible match", got)
	}
}

func TestMatchupVariantsAndShortCodes(t *testing.T) {
	home := makeSportsIdentity("nfl", "Kansas City Chiefs", "Kansas City", "Chiefs", "KC")
	away := makeSportsIdentity("nfl", "Baltimore Ravens", "Baltimore", "Ravens", "BAL")
	for _, title := range []string{"Kansas City vs Baltimore", "Ravens @ Chiefs", "Baltimore Kansas City"} {
		if evidence := scoreMatchupText(title, home, away); evidence.score < strongSportsConfidence {
			t.Fatalf("%q score = %.2f, want strong matchup", title, evidence.score)
		}
	}
	if evidence := scoreTextForTeam("Tennessee Sports", home, false); evidence.score != 0 {
		t.Fatalf("KC/short-code false positive score = %.2f", evidence.score)
	}
	newEngland := makeSportsIdentity("nfl", "New England Patriots", "New England", "Patriots", "NE")
	if evidence := scoreTextForTeam("Tennessee Sports", newEngland, false); evidence.score != 0 {
		t.Fatalf("NE matched inside Tennessee with score %.2f", evidence.score)
	}
}

func TestMatchupRequiresBothTeamsInOneSegment(t *testing.T) {
	home := makeSportsIdentity("soccer", "United States", "United States", "United States", "USA")
	away := makeSportsIdentity("soccer", "Australia", "Australia", "Australia", "AUS")
	evidence := scoreMatchupText("USA Soccer07: Australia vs Turkey", home, away)
	if evidence.score >= strongSportsConfidence {
		t.Fatalf("cross-segment evidence = %.2f, want non-strong", evidence.score)
	}
}

func TestStreamGroupingPreservesDistinctFeeds(t *testing.T) {
	matches := []models.SportsStreamMatch{
		{ChannelID: "1", ChannelName: "Live | Phillies at Angels HD", ChannelURL: "https://one", Confidence: .94, ConfidenceTier: "strong", MatchReason: "Both team nicknames"},
		{ChannelID: "2", ChannelName: "Phillies at Angels FHD", ChannelURL: "https://two", Confidence: .94, ConfidenceTier: "strong", MatchReason: "Both team nicknames"},
		{ChannelID: "3", ChannelName: "Phillies at Angels FHD", ChannelURL: "https://two", Confidence: .94, ConfidenceTier: "strong", MatchReason: "Both team nicknames"},
	}
	flat, groups := buildSportsStreamGroups(matches)
	if len(flat) != 2 {
		t.Fatalf("flat len = %d, want exact URL dedupe to 2", len(flat))
	}
	if len(groups) != 1 || groups[0].FeedCount != 2 || len(groups[0].Alternatives) != 1 {
		t.Fatalf("groups = %+v, want one group with two feeds", groups)
	}
}

func TestConservativeAutoLinkEvaluation(t *testing.T) {
	teams := []models.SportsTeamRecord{
		testTeamRecord("Washington Nationals", "Washington", "Nationals", "WSH"),
		{ID: "mlb:yankees", League: "mlb", Name: "New York Yankees", Location: "New York", Nickname: "Yankees", Abbreviation: "NYY"},
	}
	teams[0].ID = "mlb:nationals"
	channels := []LiveChannel{
		{ID: "nationals", SourceID: "one", Name: "Washington Nationals", URL: "https://nationals"},
		{ID: "ended", SourceID: "one", Name: "End | Washington Nationals", URL: "https://ended"},
		{ID: "yanks", SourceID: "one", Name: "Yanks Network", URL: "https://yanks"},
	}
	evaluation := evaluateSportsAutoLinks("mlb", teams, map[string][]models.SportsTeamChannelLink{}, channels)
	if evaluation.preview.EligibleCount != 2 || len(evaluation.links) != 2 {
		t.Fatalf("evaluation = %+v, want two eligible permanent links", evaluation.preview)
	}
	if evaluation.links[0].ChannelName == "End | Washington Nationals" {
		t.Fatal("ended feed was auto-linked")
	}

	existing := map[string][]models.SportsTeamChannelLink{
		"mlb:nationals": {{TeamID: "mlb:nationals", Slot: models.SportsLinkSlotPrimary}},
	}
	evaluation = evaluateSportsAutoLinks("mlb", teams, existing, channels)
	if evaluation.preview.EligibleCount != 1 || evaluation.preview.Results[0].SkipReason != "Primary channel already linked" {
		t.Fatalf("existing primary was not protected: %+v", evaluation.preview)
	}
}

type sportsMatcherEPG struct {
	items []models.EPGNowPlaying
}

func (f sportsMatcherEPG) GetNowPlaying(_ []string, _ ...time.Duration) []models.EPGNowPlaying {
	return f.items
}

func TestGameEPGPromotesNetworkMatch(t *testing.T) {
	game := models.SportsGame{
		League:   "mlb",
		HomeTeam: models.SportsTeam{Name: "Washington Nationals", Location: "Washington", Nickname: "Nationals", Abbreviation: "WSH"},
		AwayTeam: models.SportsTeam{Name: "New York Mets", Location: "New York", Nickname: "Mets", Abbreviation: "NYM"},
	}
	program := models.EPGProgram{Title: "New York Mets at Washington Nationals"}
	channels := []LiveChannel{{ID: "yes", Name: "YES Network", URL: "https://example/yes", TvgID: "yes.us"}}
	matches := matchGameToChannels(game, channels, sportsMatcherEPG{items: []models.EPGNowPlaying{
		{ChannelID: "yes.us", Next: &program},
	}}, "YES")
	if len(matches) != 1 || matches[0].Confidence != 0.99 || matches[0].ConfidenceTier != "strong" {
		t.Fatalf("matches = %+v, want 99%% EPG-promoted YES match", matches)
	}
}
