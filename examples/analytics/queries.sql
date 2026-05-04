-- Exercises window functions (row_number / rank), array parameters
-- with ANY, array_agg per group, and FROM-position unnest.

-- name: AddPlayer :one
INSERT INTO players (name) VALUES ($1) RETURNING *;

-- name: RecordMatch :one
INSERT INTO matches (player_id, region, score, played_at)
VALUES ($1, $2, $3, $4) RETURNING *;

-- name: TopScoresByRegion :many
-- One row per (region, rank position) with ties broken by id. Uses
-- row_number() OVER (PARTITION BY region ORDER BY score DESC).
SELECT region, player_id, score,
       row_number() OVER (PARTITION BY region ORDER BY score DESC, id) AS rn
FROM matches
ORDER BY region, rn;

-- name: RankByScore :many
-- rank() shares positions on ties.
SELECT player_id, score,
       rank() OVER (ORDER BY score DESC) AS rk
FROM matches
ORDER BY rk, player_id;

-- name: ScoresArrayPerPlayer :many
-- array_agg collects every score per player in a single bigint[].
SELECT player_id, array_agg(score)::bigint[] AS scores
FROM matches
GROUP BY player_id
ORDER BY player_id;

-- name: PlayersByIDs :many
-- Filters via `= ANY ($1::bigint[])`. The standard sqlc pattern for
-- "find these N rows."
SELECT * FROM players
WHERE id = ANY($1::bigint[])
ORDER BY id;

-- name: ExpandIDs :many
-- FROM-position unnest. Each parameter element becomes a row. The
-- explicit ::bigint cast tells sqlc the projected column's type.
SELECT u::bigint AS id
FROM unnest($1::bigint[]) AS u
ORDER BY u;
