# menged

**መንገድ** — road, way, journey.

A personal trip calculator for driving passengers around Addis Ababa. Set pickup and
destination; get driving distance, duration, route, and a suggested fare from your rate
card. Optionally save the trip and what you actually charged.

Or record the trip as you drive: menged prices the distance, moving time, and waiting
time actually driven, and keeps the quote alongside.

Not a ride-hailing platform.

```
pickup A  →  destination B  →  route preview  →  distance + duration
                                                       ↓
                              save trip  ←  suggested fare  ←  your rate card
                            (+ what you actually charged)
```

## Status

**v1 beta** — personal use behind HTTPS. Scope: [docs/mvp.md](docs/mvp.md).

## Run

```sh
cp config.example.yaml config.yaml   # edit rates
export ORS_API_KEY=...               # HeiGIT / openrouteservice.org
go run .                             # → http://127.0.0.1:8080
# or: go build -o menged . && ./menged
```

One binary, one config, one SQLite file (`menged.db`).  
Health: `GET /api/health` → `{"status":"ok"}`.

## Deploy (VPS)

Keep menged on loopback; put TLS + **Basic Auth** on the reverse proxy (no in-app tokens). The process refuses a non-loopback listen unless you pass `-allow-public-listen`.

1. `go build -o menged .` — ship binary + `config.yaml`; `ORS_API_KEY` in the environment only.
2. `server.listen: 127.0.0.1:8080`
3. nginx/Caddy: HTTPS + Basic Auth → `127.0.0.1:8080`
4. systemd (or similar). Probe: `curl -fsS -u user:pass https://your.host/api/health`
5. Backup `menged.db` (and `-wal` / `-shm` if present)

Details: [architecture.md §5](docs/architecture.md#5-exposing-it-beyond-localhost).

## Docs

| Doc | Contents |
|-----|----------|
| [docs/mvp.md](docs/mvp.md) | Scope, fare model, API, non-goals |
| [docs/architecture.md](docs/architecture.md) | Components, estimate flow, hosting |
| [docs/geospatial.md](docs/geospatial.md) | Map, routing, geocoding |
| [docs/roadmap.md](docs/roadmap.md) | Post-beta triggers |
| [docs/background.md](docs/background.md) | Why a new repo |

## Shape

- **Map**: MapLibre GL JS + OpenFreeMap
- **Routing**: HeiGIT ORS (`Router`); OSRM later via config
- **Geocoding**: HeiGIT Pelias on the ORS key
- **Storage**: SQLite (WAL)
- **Client**: static HTML/CSS/JS + `manifest.json` (no service worker)

## Non-goals

Matching, dispatch, multi-driver accounts, passenger apps, in-app payments, surge
automation, microservices. See [docs/roadmap.md](docs/roadmap.md).
