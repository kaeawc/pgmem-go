-- name: CreateUser :one
INSERT INTO users (email, name, created_at)
VALUES ($1, $2, $3)
RETURNING *;

-- name: GetUser :one
SELECT * FROM users WHERE id = $1;

-- name: ListUsers :many
SELECT * FROM users ORDER BY id;

-- name: CreatePost :one
INSERT INTO posts (author_id, title, body, published, created_at)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: GetPost :one
SELECT * FROM posts WHERE id = $1;

-- name: ListPostsByAuthor :many
SELECT * FROM posts WHERE author_id = $1 ORDER BY created_at DESC;

-- name: ListPublishedPosts :many
SELECT p.id, p.title, p.body, p.created_at, u.name AS author_name
FROM posts p
JOIN users u ON u.id = p.author_id
WHERE p.published = true
ORDER BY p.created_at DESC
LIMIT $1 OFFSET $2;

-- name: PublishPost :one
UPDATE posts SET published = true WHERE id = $1 RETURNING *;

-- name: DeletePost :exec
DELETE FROM posts WHERE id = $1;

-- name: CreateComment :one
INSERT INTO comments (post_id, author_id, body, created_at)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: ListCommentsForPost :many
SELECT c.*, u.name AS author_name
FROM comments c
JOIN users u ON u.id = c.author_id
WHERE c.post_id = $1
ORDER BY c.created_at;

-- name: CountCommentsPerPost :many
SELECT post_id, count(*) AS comment_count
FROM comments
GROUP BY post_id
ORDER BY post_id;

-- name: PostWithCommentCount :one
SELECT p.id, p.title,
       (SELECT count(*) FROM comments c WHERE c.post_id = p.id) AS comment_count
FROM posts p
WHERE p.id = $1;
