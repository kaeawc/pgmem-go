CREATE TABLE lists (
    id         bigserial   PRIMARY KEY,
    name       text        NOT NULL,
    created_at timestamptz NOT NULL,
    deleted_at timestamptz
);

CREATE TABLE items (
    id         bigserial   PRIMARY KEY,
    list_id    bigint      NOT NULL REFERENCES lists(id),
    title      text        NOT NULL,
    done       boolean     NOT NULL,
    due_at     timestamptz,
    created_at timestamptz NOT NULL,
    deleted_at timestamptz
);
