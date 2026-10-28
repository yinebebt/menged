CREATE TABLE IF NOT EXISTS trips (
    id              INTEGER PRIMARY KEY,
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

CREATE TABLE IF NOT EXISTS places (
    id         INTEGER PRIMARY KEY,
    label      TEXT NOT NULL UNIQUE,
    lat        REAL NOT NULL,
    lng        REAL NOT NULL,
    created_at TEXT NOT NULL
);
