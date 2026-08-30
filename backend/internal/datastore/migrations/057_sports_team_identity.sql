-- +goose Up
ALTER TABLE sports_teams ADD COLUMN location TEXT NOT NULL DEFAULT '';
ALTER TABLE sports_teams ADD COLUMN nickname TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE sports_teams DROP COLUMN IF EXISTS nickname;
ALTER TABLE sports_teams DROP COLUMN IF EXISTS location;
