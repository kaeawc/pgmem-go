-- Exercises: ON CONFLICT DO UPDATE upsert; soft delete via
-- `deleted_at IS NULL` filtering; timestamp + interval math; bool_and
-- aggregate per list.

-- name: UpsertList :one
INSERT INTO lists (id, name, created_at)
VALUES ($1, $2, $3)
ON CONFLICT (id) DO UPDATE SET name = excluded.name
RETURNING *;

-- name: ActiveLists :many
SELECT * FROM lists WHERE deleted_at IS NULL ORDER BY created_at DESC;

-- name: SoftDeleteList :exec
UPDATE lists SET deleted_at = $1 WHERE id = $2;

-- name: AddItem :one
INSERT INTO items (list_id, title, done, due_at, created_at)
VALUES ($1, $2, false, $3, $4)
RETURNING *;

-- name: CompleteItem :exec
UPDATE items SET done = true WHERE id = $1;

-- name: ItemsDueWithin :many
SELECT * FROM items
WHERE deleted_at IS NULL
  AND due_at IS NOT NULL
  AND due_at <= $1::timestamptz + interval '1 day'
ORDER BY due_at;

-- name: ListCompletion :many
SELECT list_id, bool_and(done) AS all_done, count(*) AS total
FROM items
WHERE deleted_at IS NULL
GROUP BY list_id
ORDER BY list_id;
