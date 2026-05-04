CREATE TABLE users (
    id   bigserial PRIMARY KEY,
    name text      NOT NULL
);

CREATE TABLE rooms (
    id   bigserial PRIMARY KEY,
    name text      NOT NULL
);

CREATE TABLE subscriptions (
    user_id bigint NOT NULL REFERENCES users(id),
    room_id bigint NOT NULL REFERENCES rooms(id),
    PRIMARY KEY (user_id, room_id)
);

CREATE TABLE messages (
    id        bigserial   PRIMARY KEY,
    room_id   bigint      NOT NULL REFERENCES rooms(id),
    author_id bigint      NOT NULL REFERENCES users(id),
    parent_id bigint      REFERENCES messages(id),
    body      text        NOT NULL,
    sent_at   timestamptz NOT NULL
);

CREATE TABLE system_messages (
    id      bigserial   PRIMARY KEY,
    room_id bigint      NOT NULL REFERENCES rooms(id),
    body    text        NOT NULL,
    sent_at timestamptz NOT NULL
);
