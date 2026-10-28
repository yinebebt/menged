# Architecture

The guiding rule: **every component has to justify its own existence.** This document
records what's in, what's out, and why.

---

## 1. The whole thing

```
  browser (MapLibre GL JS ← OpenFreeMap tiles)
       │  JSON /api/*
       ▼
  menged (one Go binary)
    handlers · fare · Router · Geocoder · SQLite
       │                    │
       ▼                    ▼
  ORS (+ Pelias geo)     menged.db
```

One process, one config, one SQLite file.

---

## 2. Component decisions

### Backend: a small Go binary — **yes**

Not for scale. For two specific reasons:

1. **The routing API key must not sit in the browser.** A key in client-side JavaScript
   is a key anyone can lift.
2. **Trip history has to survive.** Browser storage gets cleared, and you'd lose your
   records.

A third reason emerges later: the fare must have exactly one owner. The old backend let
clients submit their own distance and price and trusted them — that bug is avoided by
construction if the server does the arithmetic.

### Database: SQLite — **yes**, Postgres — **no**

One user, one writer, a few thousand rows a year. SQLite is a file. No daemon, no
container, no connection pool, no migration tooling. Backup is `cp menged.db elsewhere`.

Use `modernc.org/sqlite` (pure Go, no cgo) so the binary cross-compiles cleanly and you
can drop it on a phone-sized ARM box without a toolchain.

### Web framework — **no**

`net/http` with Go 1.22+ method-and-path routing (`mux.HandleFunc("POST /api/trips", …)`)
covers the small API surface comfortably. Gin buys nothing here.

### Frontend build step — **no**

One `index.html`, one `app.js`, one `style.css`, MapLibre from a CDN or vendored. No npm,
no bundler, no framework. Embed them with `//go:embed` so the binary is self-contained.

`manifest.json` supports a home-screen bookmark look. No service worker until offline
shell caching earns its keep ([roadmap.md](roadmap.md) §1.4).

### Redis, MinIO, Casbin, JWT, WebSockets, worker pools — **no**

There is no concurrency to manage, no second user to authorise, no file to store, and no
real-time channel in the workflow. Every one of these was load-bearing in the old
ride-sharing backend and none of them are load-bearing here.

### Authentication — **no** (in v1)

It binds to `127.0.0.1`. If it's only reachable from the machine it runs on, a login
screen protects nothing and costs you a password prompt every time you're in traffic.

### Routing behind an interface — **yes**, and this is the one abstraction worth having

```go
type Router interface {
    Route(ctx context.Context, from, to Point) (Route, error)
}

type Route struct {
    DistanceKM  float64
    DurationMin float64
    Geometry    string // encoded polyline
}
```

Two implementations: `ors` (hosted, needs a key, 2 000 requests/day free) and `osrm`
(self-hosted, unlimited). Starting with ORS means you're running today; switching to your
own OSRM later is a config change and one new file, not a rewrite. See
[geospatial.md](geospatial.md).

---

## 3. Layout

Flat, one `package main`. Packages can be extracted later if the tree grows.

```
menged/
├── main.go / config.go / handlers.go
├── fare.go (+ fare_test.go)     # pure fare arithmetic
├── geo.go (+ geo_test.go)       # Router / Geocoder interfaces, service area,
│                                # route + reverse caches, typed geo errors
├── ors.go                       # HeiGIT ORS directions + Pelias geocoding
├── osrm.go                      # self-hosted OSRM directions
├── track.go (+ track_test.go)   # recorded GPS track → distance, moving and waiting time
├── store.go / schema.sql        # SQLite trips + places
├── config.example.yaml
├── web/                         # //go:embed static UI
└── docs/
```

Tests cover the pure units (`fare.go`, `track.go`), the store against a temp SQLite
file, and the estimate/measure handlers with a stub router. Deps: `modernc.org/sqlite`,
`gopkg.in/yaml.v3`.

---

## 4. Estimate request flow

1. Browser has both pins → `POST /api/estimate {pickup, stop?, dest, waiting_min?}`.
2. Handler validates coordinates via `InServiceArea` (Ethiopia-ish bbox in `geo.go`).
3. `router.Route(points)` (pickup, optional stop, destination) → distance, duration, encoded polyline. `NewRouter` wraps the
   provider with an in-memory cache (~10 m–rounded keys, few minutes TTL) and one retry
   only on transient errors (not ORS 4xx / unroutable pins).
4. `CalculateFare(...)` → amount + breakdown.
5. Client decodes the polyline onto MapLibre (waits for map load if needed).

ORS snap failures unwrap to `ErrUnroutable` (HTTP 422); handlers do not string-match ORS JSON.

**Recorded trips.** "Start trip" collects GPS fixes in the browser (one per ≥2 s, kept
in `localStorage` so a reload resumes) while a screen wake lock is held. About every
30 s the live meter posts the track to `POST /api/measure` with `route: false` (no
routing call). "End trip" posts it with `route: true`: `measureTrack` in `track.go`
drops fixes outside the service area or worse than 50 m (unless none are better),
counts only moves larger than a fix's accuracy, and splits time into moving and
waiting at 5 km/h; the router then returns the road distance through points sampled
along the track. The server prices it with the same `CalculateFare`. A web page gets no
GPS in the background, so recording needs menged on screen.

---

## 5. Exposing it beyond localhost

You will eventually want this on your phone while the binary lives on a small server.
In rough order of preference:

1. **Tailscale** — private IP only, no public exposure. Best effort-to-safety ratio.
2. **Reverse proxy + Basic Auth (recommended for a public VPS)** — bind menged to
   `127.0.0.1:8080`, put nginx or Caddy in front with HTTPS and `auth_basic`. There is
   no in-app bearer token; protect the host at the proxy.
3. **A real login** — only if a second person ever uses it; see [roadmap.md](roadmap.md).

Do not put it on a public IP with no auth. The routing quota is attached to your key.
Startup refuses a non-loopback listen unless you pass `-allow-public-listen` (which still logs a warning).

---

## 6. Settled product choices

- Polyline: Google-encoded precision-5 (what ORS returns); client decodes for the map.
- Config: YAML with `${ENV}` expansion (`ORS_API_KEY`).
- History: full-screen panel over the estimate UI; tap a row for detail, then show on
  map / repeat / edit.
