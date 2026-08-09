CREATE TABLE IF NOT EXISTS trips (
    id              INTEGER PRIMARY KEY,
    created_at      TEXT    NOT NULL,          -- RFC3339, local time

    pickup_label    TEXT,                      -- "Home", or blank for a raw map tap
    pickup_lat      REAL    NOT NULL,
    pickup_lng      REAL    NOT NULL,
    dest_label      TEXT,
    dest_lat        REAL    NOT NULL,
    dest_lng        REAL    NOT NULL,

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

    note            TEXT
);

CREATE TABLE IF NOT EXISTS places (
    id         INTEGER PRIMARY KEY,
    label      TEXT NOT NULL UNIQUE,
    lat        REAL NOT NULL,
    lng        REAL NOT NULL,
    created_at TEXT NOT NULL
);
