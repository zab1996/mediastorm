-- +goose Up
CREATE TABLE sports_teams (
    id            TEXT PRIMARY KEY,        -- "{league}:{espn_team_id}", e.g. "mlb:135"
    league        TEXT NOT NULL,
    espn_team_id  TEXT NOT NULL,
    name          TEXT NOT NULL,
    abbreviation  TEXT NOT NULL DEFAULT '',
    logo_url      TEXT NOT NULL DEFAULT '',
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (league, espn_team_id)
);

CREATE TABLE sports_team_channel_links (
    id                TEXT PRIMARY KEY,
    team_id           TEXT NOT NULL REFERENCES sports_teams(id) ON DELETE CASCADE,
    slot              TEXT NOT NULL CHECK (slot IN ('primary', 'backup')),
    position          INT NOT NULL DEFAULT 0,
    -- Denormalized channel snapshot rather than a bare foreign key: M3U/Xtream channel IDs
    -- are regenerated on every playlist parse and aren't durable across refreshes/reordering
    -- (see backend/handlers/live.go parseM3UPlaylist), so links resolve at read time by
    -- re-matching channel_tvg_id/channel_name against the live channel list, self-healing
    -- if the provider's IDs drift, rather than pointing at an ID that can silently go stale.
    channel_tvg_id    TEXT NOT NULL DEFAULT '',
    channel_name      TEXT NOT NULL,
    channel_url       TEXT NOT NULL,
    channel_logo      TEXT NOT NULL DEFAULT '',
    source_id         TEXT NOT NULL DEFAULT '',
    source_name       TEXT NOT NULL DEFAULT '',
    last_verified_at  TIMESTAMPTZ,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (team_id, slot, position)
);

CREATE INDEX idx_sports_team_channel_links_team ON sports_team_channel_links(team_id);
CREATE INDEX idx_sports_team_channel_links_tvg ON sports_team_channel_links(channel_tvg_id) WHERE channel_tvg_id <> '';

-- +goose Down
DROP TABLE IF EXISTS sports_team_channel_links;
DROP TABLE IF EXISTS sports_teams;
