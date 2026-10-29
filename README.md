# menged

**መንገድ** (road, way, journey): a trip calculator for drivers in Addis Ababa.

Drop a pickup and a destination on the map and menged shows the driving distance, the
time, the route, and a fare from your own rate card. Or start a trip and let it record
the drive: the fare then comes from the distance actually driven and the time spent
waiting. Trips you save keep what you actually charged, so over time you can see how
your rates compare with what passengers pay.

It is a tool for one driver or a few, not a ride-hailing platform: there is no matching,
dispatch or passenger app.

## Features

- **Quotes**: pickup, an optional stop, and a destination, from a map tap, GPS, place
  search or a saved place. Distance, duration and fare update as you move the pins.
- **Trip recording**: browser GPS while menged is on screen, with a live meter. Moving
  and waiting time are priced separately.
- **Sharing**: send a quote ("Bole → Ayat, 14.0 km, ~35 min, ETB 240") with a Google
  Maps link through the phone's share sheet.
- **Trip log**: saved trips with what was charged, monthly totals, and the rate card used
  for each trip, so old trips still add up after you change your rates.
- **Accounts**: email and password, reset by email. Quotes work without signing in.

## Quick start

You need Go 1.26+ and a free [openrouteservice](https://openrouteservice.org/dev/#/signup)
API key.

```sh
cp config.example.yaml config.yaml   # set your rates
export ORS_API_KEY=...
go run .                             # http://127.0.0.1:8080
```

menged is one binary with the web app embedded, one YAML config and one SQLite file
(`menged.db`, created on first run). Password-reset emails go to the server log until you
configure mail.

## Configuration

Every setting is in [config.example.yaml](config.example.yaml) with a comment. The main
groups:

| Section | What it sets |
|---------|--------------|
| `fare` | Currency, base fare, per-km, per-minute and per-waiting-minute rates, minimum fare, rounding |
| `routing` | `ors` (hosted openrouteservice) or `osrm` (self-hosted) |
| `geocoding` | `pelias` (uses the ORS key) or `off` |
| `server` | Listen address, database path, map style, whether sign-up is open, public URL |
| `mail` | `log` or `mailtrap` for password-reset emails |

`${VAR}` in the file is replaced from the environment, so keys stay out of the config.

## Running it on a server

Keep menged on loopback behind a TLS reverse proxy that sets `X-Forwarded-Proto`; it
refuses a public listen address unless you pass `-allow-public-listen`. Rate-limit the
sign-in endpoints and the ones that spend routing quota at the proxy. The whole state is
`menged.db`, so backing up that file is enough.

## Documentation

| | |
|-|-|
| [docs/architecture.md](docs/architecture.md) | How the pieces fit: server, storage, web app, sign-in |
| [docs/fares.md](docs/fares.md) | The fare formula and how recorded trips are measured |
| [docs/geospatial.md](docs/geospatial.md) | Map tiles, routing and geocoding providers |
| [docs/roadmap.md](docs/roadmap.md) | What might come next, and what is out of scope |
| [docs/openapi.yaml](docs/openapi.yaml) | HTTP API |
| [schema.sql](schema.sql) | Database schema |

## Development

```sh
gofmt -l . && go vet ./... && go test -race ./...
```

CI runs the same, plus a lint of `docs/openapi.yaml`. The web app is plain HTML, CSS and JavaScript in `web/`, with no build step. Edit and
reload.

## License

[MIT](LICENSE)
