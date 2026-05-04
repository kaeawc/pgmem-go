-- Exercises: sum() GROUP BY for on-hand; CTEs for per-warehouse
-- rollup; CASE WHEN for low-stock flagging; self-referential FK on
-- categories.

-- name: AddProduct :one
INSERT INTO products (category_id, name, threshold)
VALUES ($1, $2, $3) RETURNING *;

-- name: RecordTxn :one
INSERT INTO stock_txns (product_id, warehouse_id, delta, occurred_at)
VALUES ($1, $2, $3, $4) RETURNING *;

-- name: OnHandByProduct :many
SELECT product_id, sum(delta)::int AS qty
FROM stock_txns
GROUP BY product_id
ORDER BY product_id;

-- name: LowStockReport :many
WITH on_hand AS (
    SELECT product_id, sum(delta)::int AS qty
    FROM stock_txns
    GROUP BY product_id
)
SELECT p.id, p.name,
       coalesce(o.qty, 0) AS qty,
       CASE WHEN coalesce(o.qty, 0) < p.threshold THEN 'low' ELSE 'ok' END AS status
FROM products p
LEFT JOIN on_hand o ON o.product_id = p.id
ORDER BY status, p.name;

-- name: WarehouseRollup :many
SELECT warehouse_id, sum(delta)::int AS qty
FROM stock_txns
GROUP BY warehouse_id
ORDER BY warehouse_id;

-- name: ChildCategories :many
SELECT * FROM categories WHERE parent_id = $1 ORDER BY name;
