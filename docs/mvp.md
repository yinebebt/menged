# MVP scope

This is the contract for v1. If something is not in this document, it is not in v1.

The test for "done" is simple: **can I use this on my phone, in the car, to quote a
passenger a price before we set off, and see last month's trips afterwards?**

---

## 1. The workflow

```
1. Set pickup       tap the map, GPS, saved place, or search
2. Set destination  tap the map, saved place, or search
3. Route appears    drawn on the map once both pins are set
4. Read the numbers distance (km), duration (min), suggested fare (ETB)
5. Adjust if needed multiplier buttons; set the amount you actually charged
6. Save             optional — stored in SQLite
```

Everything happens on one screen. There is no wizard, no step navigation, no submit
button for the estimate — the estimate recomputes whenever both pins are set.

---

## 2. Screens

There are **two**.

### 2.1 Estimate (the main screen)

**Map area** (most of the screen)

- MapLibre map, centred on Addis Ababa on first load, then on your last known position.
- Two draggable pins: green for pickup, red for destination.
- The driving route drawn as a line between them once both are set.
- The map auto-fits to show the whole route when it changes.

**Setting a point**

- Tap the map → sets whichever pin is "active" (pickup first, then destination).
- A crosshair/"use my location" button fills pickup from the browser's geolocation.
- A row of saved-place chips ("Home", "Bole Airport", "Megenagna", …) sets either pin
  in one tap. See §5.
- A swap button (⇅) exchanges pickup and destination.
- Long-press a pin, or a small ✕, clears it.

**Result card** (bottom; wider centered bar on desktop)

```
  12.4 km · 28 min   [×1] [×1.25] [×1.5]   Suggested ETB 205   [Save]
```

- Distance to one decimal place, duration to the nearest minute.
- Fare rounded to the nearest 5 ETB (configurable).
- Multipliers are manual (night, rain, traffic) — not automatic surge. Default ×1.
- Spinner while routing; plain error + retry on failure. No silent straight-line fallback.

**Save trip** opens a small sheet:

- Amount charged — prefilled with the suggested fare, editable. This is the important field.
- Note — free text, optional (passenger name, "waited 20 min", "return trip").
- Save / Cancel.

### 2.2 History

- Reverse-chronological list of saved trips. Each row: date, pickup → destination labels,
  km, duration, suggested vs. charged.
- A header with totals for the current month: trip count, total km, total charged.
- Tap a row for trip detail (rate card, note, charged); from there show the saved route
  on the map, repeat (re-quote), edit, or delete.

No charts, CSV export, or history filters in v1. Backup is the SQLite file.

---

## 3. Fare model

Kept intentionally boring and fully configurable.

```
raw   = base_fare + (distance_km × price_per_km) + (duration_min × price_per_minute)
        + (waiting_min × price_per_waiting_minute)
fare  = max(minimum_fare, raw) × multiplier
fare  = round_to_nearest(fare, rounding)
```

`price_per_minute` exists because a 6 km trip through Bole at 6 PM and the same trip at
6 AM are not the same job. Set it to `0` to ignore time entirely.

`waiting_min` is typed in by the driver (time stopped for the passenger or at a stop) and
priced separately from driving time. Set `price_per_waiting_minute` to `0` to hide it.

Example config (`config.yaml`, edited by hand, read at startup):

See [config.example.yaml](../config.example.yaml) for the live shape (`provider: ors`,
HeiGIT default `base_url`).

**Every saved trip stores the rates that were used to price it**, not just the result.
When you raise your per-km rate next year, last year's trips must still make sense.

---

## 4. Data model

SQLite. Two tables. No migration framework — a `schema.sql` applied with
`CREATE TABLE IF NOT EXISTS` at startup is enough at this size.

```sql
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
```

`suggested_fare` next to `actual_charged` is the most valuable thing in this schema.
After fifty trips it tells you whether your rate card matches what people actually pay —
which is information you cannot get any other way.

---

## 5. Saved places (primary) + assistive geocoding

Addis navigation is landmark-based. **Saved places** (name a pin → one-tap chip) stay the
fastest path for places you reuse.

**Assistive geocoding** (shipped): unlabeled pins (tap / drag / GPS) are reverse-labeled
automatically; search is a centered dialog. Provider: HeiGIT Pelias on the ORS key.
Details: [geospatial.md §5](geospatial.md#5-geocoding-shipped).

---

## 6. HTTP surface

Small enough to list completely.

| Method | Path | Purpose |
|--------|------|---------|
| `GET` | `/` | serves the single-page app (embedded static files) |
| `GET` | `/api/health` | liveness probe (`{"status":"ok"}`), open even when auth is on |
| `GET` | `/api/config` | fare rates, map style/camera, `geocode` flag |
| `POST` | `/api/estimate` | `{pickup:{lat,lng}, stop?:{lat,lng}, dest:{lat,lng}, waiting_min?, multiplier?}` → `{distance_km, duration_min, geometry, fare, breakdown}` |
| `GET` | `/api/geocode` | `?q=&lat=&lng=` → place hits (when geocoding configured) |
| `GET` | `/api/reverse` | `?lat=&lng=` → `{label}` for a pin |
| `GET` | `/api/trips` | list, newest first |
| `POST` | `/api/trips` | save a trip |
| `PATCH` | `/api/trips/{id}` | edit `actual_charged` / `note` |
| `DELETE` | `/api/trips/{id}` | delete a trip |
| `GET` | `/api/places` | list saved places |
| `POST` | `/api/places` | add one |
| `DELETE` | `/api/places/{id}` | remove one |

**The fare is calculated on the server**, in `/api/estimate`, and the client only
displays it. The old ride-sharing backend let the client send its own distance and price
and stored them unquestioned — one number, one owner, server side.

No in-app auth: bind to `127.0.0.1` locally; on a VPS use reverse-proxy Basic Auth — see
[architecture.md §5](architecture.md#5-exposing-it-beyond-localhost).

---

## 7. Explicitly not in v1

- Live GPS tracking of the trip, or recording the route actually driven
- More than one stop, or waiting measured from GPS (typed-in waiting and one stop are in)
- Accounts, login, or more than one driver
- Passenger-facing anything; payments, receipts, SMS
- Automatic surge / time-of-day pricing (multiplier stays manual)
- Offline routing or offline map tiles
- Native mobile app / MapLibre Native
- Sync between devices; charts beyond the monthly totals line

---

## 8. Beta checklist

- [x] Map of Addis loads quickly on phone
- [x] Pickup from GPS; destination by tap / search / saved place
- [x] Route line + distance/duration from ORS
- [x] Suggested fare matches the config rate card
- [x] Save trip with actual charged amount
- [x] History with month totals; trip detail; show on map / repeat / edit
- [x] Data survives server restart (SQLite)
- [x] Usable one-handed in a phone browser
- [x] `config.yaml` + `go run .` (or one binary) is enough to run
