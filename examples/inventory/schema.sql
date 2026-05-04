CREATE TABLE categories (
    id        bigserial PRIMARY KEY,
    parent_id bigint    REFERENCES categories(id),
    name      text      NOT NULL
);

CREATE TABLE products (
    id          bigserial PRIMARY KEY,
    category_id bigint    NOT NULL REFERENCES categories(id),
    name        text      NOT NULL,
    threshold   int       NOT NULL
);

CREATE TABLE warehouses (
    id   bigserial PRIMARY KEY,
    name text      NOT NULL
);

CREATE TABLE stock_txns (
    id           bigserial   PRIMARY KEY,
    product_id   bigint      NOT NULL REFERENCES products(id),
    warehouse_id bigint      NOT NULL REFERENCES warehouses(id),
    delta        int         NOT NULL,
    occurred_at  timestamptz NOT NULL
);
