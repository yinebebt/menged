-- Users sign up in the app with an email; a forgotten password is reset by an
-- emailed link (or menged -reset-password EMAIL on the server).
CREATE TABLE IF NOT EXISTS users (
    id              INTEGER PRIMARY KEY,
    email           TEXT    NOT NULL UNIQUE COLLATE NOCASE,
    password_hash   TEXT    NOT NULL,          -- bcrypt
    created_at      TEXT    NOT NULL
);

CREATE TABLE IF NOT EXISTS password_resets (
    token_hash      TEXT    PRIMARY KEY,       -- sha256 of the emailed token, hex
    user_id         INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at      TEXT    NOT NULL           -- RFC3339 UTC; one hour after issue
);

CREATE TABLE IF NOT EXISTS sessions (
    token_hash      TEXT    PRIMARY KEY,       -- sha256 of the cookie value, hex
    user_id         INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at      TEXT    NOT NULL,
    expires_at      TEXT    NOT NULL           -- RFC3339 UTC
);

CREATE TABLE IF NOT EXISTS trips (
    id              INTEGER PRIMARY KEY,
    user_id         INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at      TEXT    NOT NULL,          -- RFC3339, local time

    pickup_label    TEXT,                      -- "Home", or blank for a raw map tap
    pickup_lat      REAL    NOT NULL,
    pickup_lng      REAL    NOT NULL,
    dest_label      TEXT,
    dest_lat        REAL    NOT NULL,
    dest_lng        REAL    NOT NULL,
    stop_label      TEXT,                      -- optional intermediate stop
    stop_lat        REAL,
    stop_lng        REAL,
    waiting_min     REAL    NOT NULL DEFAULT 0,

    distance_km     REAL    NOT NULL,
    duration_min    REAL    NOT NULL,
    route_geometry  TEXT,                      -- encoded polyline, for redrawing later

    suggested_fare  REAL    NOT NULL,
    actual_charged  REAL,                      -- null = not recorded
    multiplier      REAL    NOT NULL DEFAULT 1,

    -- rate card snapshot, so old trips stay explainable
    rate_base       REAL    NOT NULL,
    rate_per_km     REAL    NOT NULL,
    rate_per_min    REAL    NOT NULL,
    rate_per_wait_min REAL  NOT NULL DEFAULT 0,

    note            TEXT,

    -- recorded trips only: distance/duration/waiting/suggested_fare above are measured
    started_at      TEXT,                      -- RFC3339
    ended_at        TEXT,
    track           TEXT,                      -- polyline of the GPS fixes that counted
    quoted_fare     REAL                       -- the quote given before starting, if any
);

CREATE INDEX IF NOT EXISTS trips_user_created ON trips (user_id, created_at);

CREATE TABLE IF NOT EXISTS places (
    id         INTEGER PRIMARY KEY,
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    label      TEXT NOT NULL,
    lat        REAL NOT NULL,
    lng        REAL NOT NULL,
    created_at TEXT NOT NULL,
    UNIQUE (user_id, label)
);
