# Maps, routing and geocoding

External capabilities: **map display**, **driving route**, **place labels / search**.

Limits below were checked **August 2026**; re-verify before relying on free tiers.

---

## 1. What we need

| Need | Used for | Status |
|------|----------|--------|
| Map display | pins + route preview | OpenFreeMap + MapLibre GL JS |
| Driving route | distance, duration, geometry | HeiGIT ORS (OSRM later) |
| Geocoding | reverse labels + search | Pelias on ORS key |
| Saved places | one-tap landmarks | primary UX (local SQLite) |

Volume is low (tens of estimates/day) — free ORS/Pelias tiers are enough.

---

## 2. Map display

**Choice: MapLibre GL JS + OpenFreeMap** (`https://tiles.openfreemap.org/styles/liberty`).

No map API key. Keep the MapLibre OSM attribution control. Style URL is
`server.map_style` in config. Camera defaults (center, zoom, maxBounds) come from
`/api/config`.

| Alternative | Why not |
|-------------|---------|
| Mapbox / Google | account + key in the page |
| Leaflet + OSM raster | workable fallback; weaker phone look |

**Not using MapLibre Native** — web UI only unless you ship a store app
([roadmap.md](roadmap.md)).

---

## 3. Driving route

**Choice: HeiGIT OpenRouteService** via `Router` (`ors` / `openrouteservice`).

- Base: `https://api.heigit.org/openrouteservice` (legacy `api.openrouteservice.org` retired)
- Auth: `Authorization: <ORS_API_KEY>`
- Snap: `radiuses: [5000, 5000]` metres so map taps off the exact centreline still route
- Map pan `maxBounds` kept over road-dense Greater Addis (see `/api/config`)
- Free tier is enough for personal volume; self-host **OSRM** later via
  `routing.provider: osrm` + `base_url`

Matrix / isochrones / optimization APIs are out of scope. Unroutable pins return a clear
422 (“no road near that pin”) instead of a generic gateway error.

---

## 4. Why saved places still matter

Addis is landmark-navigated; OSM-backed geocoders are uneven on informal names. Saved
places cover your real stops faster than any search box. Remote geocoding (§5) is assistive.

| Option | Note |
|--------|------|
| HeiGIT Pelias | **shipped** — zero ops; same ORS key; ~3 000/day |
| GebetaMaps | strong local candidate; keys often gated — **not wired in code**; revisit if you get API access ([docs.gebeta.app](https://docs.gebeta.app/)) |
| Public Nominatim / Photon demo | not for production traffic |
| Self-hosted Photon | later independence path |

---

## 5. Geocoding (shipped — Pelias)

```go
type Geocoder interface {
    Autocomplete(ctx context.Context, q string, near Point) ([]PlaceHit, error)
    Reverse(ctx context.Context, p Point) (string, error)
}
```

Uses `routing.api_key` against `https://api.heigit.org/pelias/v1` (or `geocoding.pelias_base_url`).
`geocoding.provider`: `pelias` (default) | `off`.

| Path | Purpose |
|------|---------|
| `GET /api/geocode?q=&lat=&lng=` | autocomplete hits |
| `GET /api/reverse?lat=&lng=` | best label for a pin |

Reverse responses cached ~50 m / 30 min (pin nudges). Client: reverse-labels map taps/drags/GPS
(card shows “Looking up…”, then a short name + pin popup); search is debounced and ranks
saved places above remote.
`/api/config` → `geocode: true` when a provider is wired.

---

## 6. Attribution

Keep MapLibre’s OpenStreetMap attribution. Credit OpenFreeMap per its licence.

---

## 7. References

- OpenFreeMap — https://openfreemap.org/
- MapLibre GL JS — https://maplibre.org/
- HeiGIT ORS — https://api.heigit.org/ · [URL migration](https://ask.openrouteservice.org/t/deprecating-api-openrouteservice-org-in-favour-of-api-heigit-org/7912)
- Pelias (ORS) — https://openrouteservice.org/dev/#/api-docs/geocode
- GebetaMaps (optional later) — https://docs.gebeta.app/
- OSRM / Geofabrik Ethiopia — https://project-osrm.org/ · https://download.geofabrik.de/africa/ethiopia.html
- Photon — https://github.com/komoot/photon
