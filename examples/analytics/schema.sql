CREATE TABLE players (
    id   bigserial PRIMARY KEY,
    name text      NOT NULL
);

CREATE TABLE matches (
    id         bigserial   PRIMARY KEY,
    player_id  bigint      NOT NULL REFERENCES players(id),
    region     text        NOT NULL,
    score      int         NOT NULL,
    played_at  timestamptz NOT NULL
);
