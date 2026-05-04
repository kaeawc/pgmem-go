-- Exercises: jsonb @> and ->>; date_trunc + EXTRACT; regex match on
-- the message column.

-- name: RecordEvent :one
INSERT INTO events (kind, body, message, created_at)
VALUES ($1, $2, $3, $4) RETURNING *;

-- name: EventsByPayloadKey :many
SELECT * FROM events
WHERE body @> $1::jsonb
ORDER BY created_at;

-- name: EventsByKindField :many
SELECT id, body ->> 'user_id' AS user_id, message
FROM events
WHERE kind = $1
ORDER BY created_at DESC
LIMIT $2;

-- name: ErrorEvents :many
SELECT * FROM events
WHERE message ~* 'error'
ORDER BY created_at DESC;

-- name: EventsPerHour :many
SELECT date_trunc('hour', created_at) AS hour,
       count(*) AS n
FROM events
GROUP BY hour
ORDER BY hour;

-- name: EventEpochs :many
SELECT id, extract(epoch FROM created_at) AS epoch
FROM events
ORDER BY id;
