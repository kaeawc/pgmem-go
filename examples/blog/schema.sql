CREATE TABLE users (
    id        bigserial   PRIMARY KEY,
    email     text        NOT NULL UNIQUE,
    name      text        NOT NULL,
    created_at timestamptz NOT NULL
);

CREATE TABLE posts (
    id         bigserial   PRIMARY KEY,
    author_id  bigint      NOT NULL REFERENCES users(id),
    title      text        NOT NULL,
    body       text        NOT NULL,
    published  boolean     NOT NULL,
    created_at timestamptz NOT NULL
);

CREATE TABLE comments (
    id         bigserial   PRIMARY KEY,
    post_id    bigint      NOT NULL REFERENCES posts(id),
    author_id  bigint      NOT NULL REFERENCES users(id),
    body       text        NOT NULL,
    created_at timestamptz NOT NULL
);
