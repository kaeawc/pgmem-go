-- Exercises: UNION ALL across user + system messages; EXISTS for
-- subscription gating; self-join on messages for thread replies;
-- LIMIT/OFFSET pagination.

-- name: AddUser :one
INSERT INTO users (name) VALUES ($1) RETURNING *;

-- name: AddRoom :one
INSERT INTO rooms (name) VALUES ($1) RETURNING *;

-- name: PostMessage :one
INSERT INTO messages (room_id, author_id, parent_id, body, sent_at)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: PostSystemMessage :one
INSERT INTO system_messages (room_id, body, sent_at)
VALUES ($1, $2, $3) RETURNING *;

-- name: Subscribe :exec
INSERT INTO subscriptions (user_id, room_id) VALUES ($1, $2)
ON CONFLICT (user_id, room_id) DO NOTHING;

-- name: RoomFeed :many
SELECT m.id, m.body, m.sent_at, 'user' AS source
FROM messages m
WHERE m.room_id = $1
UNION ALL
SELECT s.id, s.body, s.sent_at, 'system' AS source
FROM system_messages s
WHERE s.room_id = $1
ORDER BY sent_at DESC
LIMIT $2 OFFSET $3;

-- name: SubscribedRooms :many
SELECT r.id, r.name FROM rooms r
WHERE EXISTS (
    SELECT 1 FROM subscriptions s
    WHERE s.user_id = $1 AND s.room_id = r.id
)
ORDER BY r.name;

-- name: ReplyThread :many
SELECT m.id, m.body AS reply_body, m.sent_at,
       parent.body AS parent_body
FROM messages m
JOIN messages parent ON parent.id = m.parent_id
WHERE m.parent_id = $1 OR parent.id = $1
ORDER BY m.sent_at;
