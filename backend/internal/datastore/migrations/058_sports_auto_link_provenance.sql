-- +goose Up
ALTER TABLE sports_team_channel_links
    ADD COLUMN auto_linked BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN link_confidence DOUBLE PRECISION NOT NULL DEFAULT 0,
    ADD COLUMN match_reason TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE sports_team_channel_links
    DROP COLUMN IF EXISTS match_reason,
    DROP COLUMN IF EXISTS link_confidence,
    DROP COLUMN IF EXISTS auto_linked;
