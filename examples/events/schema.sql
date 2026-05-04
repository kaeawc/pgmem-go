CREATE TABLE events (
    id         bigserial   PRIMARY KEY,
    kind       text        NOT NULL,
    body       jsonb       NOT NULL,
    message    text        NOT NULL,
    created_at timestamptz NOT NULL
);
