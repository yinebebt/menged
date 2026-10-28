# After the MVP

**Nothing here is v1.** This exists so that the MVP can be built with confidence that it
isn't a dead end — each item below is reachable from the v1 design without a rewrite.

Every item has a **trigger**: the observable condition that makes it worth doing. If the
trigger hasn't happened, the work hasn't earned its place. The failure mode this document
is guarding against is building stage 3 during stage 1.

---

## Stage 1 — Make the MVP dependable

Small, unglamorous work that turns "it runs on my laptop" into "I rely on it daily".

### 1.1 Self-hosted OSRM

**Trigger:** you hit the ORS daily quota, ORS is slow or down when you need a quote, or
you simply want no third party in the loop.

**Work:** a `docker-compose.yml` with `osrm-routed` on the Ethiopia extract, plus a
`Makefile` target that downloads and preprocesses it. Flip `routing.provider: osrm` in
config. The `Router` interface already exists, so this is one new file and no changes
anywhere else. Details in [geospatial.md §3](geospatial.md#3-driving-route-distance-and-duration).

**Bonus:** if it runs on the same machine, estimates work without internet — which
matters more than it sounds when mobile data is unreliable.

### 1.2 Reachable from the phone, safely

**Trigger:** the moment the binary stops living on the device you're holding.

**Work:** Tailscale, or HTTPS reverse proxy with Basic Auth (bind menged to
`127.0.0.1`). See [architecture.md §5](architecture.md#5-exposing-it-beyond-localhost).
Do not leave it on a public IP with no auth — the routing key is attached to your account.

### 1.3 Backups you don't have to think about

**Trigger:** the first time the trip history is worth more than the effort of protecting it —
about a month in.

**Work:** a nightly `VACUUM INTO` of the SQLite file to a dated copy, plus rsync/rclone
somewhere off the machine. Ten lines of shell and a cron entry.

### 1.4 Offline shell

**Trigger:** you've opened it in a dead zone and got a blank page.

**Work:** add a service worker that caches the app shell (network-first so updates aren't
stuck), optionally recent map tiles, and queue trip saves made while offline for replay
on reconnect. Not shipped yet — dropped until this trigger is real. The estimate itself
still needs a router.

---

## Stage 2 — Make the numbers better

This is where the tool starts earning its keep, because it starts learning from your own
data rather than just doing arithmetic.

### 2.1 Fare calibration from your own history

**Trigger:** ~50 saved trips with `actual_charged` filled in.

The MVP already stores suggested fare, actual charged, and the rate card used, on every
trip. That's a dataset. A simple report — "your suggested fare was on average 12% below
what you actually charged; a per-km rate of 13.4 would have matched" — turns guesswork
into a rate card grounded in reality. This is the single highest-value thing on this
entire list, and it costs almost nothing because the data is already being collected.

### 2.2 Duration correction by time of day

**Trigger:** you notice quoted durations are consistently optimistic in rush hour.

No routing engine available to you has live Addis traffic. But if you record when a trip
started and roughly how long it actually took, you can build your own correction factor
per hour-of-day, per corridor. Half a dozen buckets would beat any free traffic source.

Requires adding an actual-duration field — cheap, and best done as part of 2.3.

### 2.3 Record the trip actually driven

**Trigger:** you want to bill for the route taken rather than the route quoted, or
disputes about distance start happening.

**Work:** a "start trip" button that samples browser GPS while driving, stores the track,
and computes real distance on completion. Optionally snap the track to roads (OSRM's
`match` service) to clean up GPS noise.

Note the trap the old ride-hailing backend fell into: it summed great-circle distances
between raw GPS samples, which both undercounts (it ignores road curvature) and
overcounts (GPS jitter at standstill adds phantom metres). Snap-to-road, or don't bother.

### 2.4 Waiting time and stops — **typed-in waiting and one stop shipped**

Shipped: a waiting-minutes field priced by `price_per_waiting_minute` (snapshotted on each
trip as `rate_per_wait_min`), and one optional stop sent to the router as a waypoint.
Still open: measuring waiting from GPS (below), which needs 2.3.

**Trigger:** you've had enough trips where you waited, or made an intermediate stop, that
the quoted fare was visibly wrong.

**Work:** waiting minutes as a field with its own rate; intermediate waypoints in the
route request (every router supports them). Both are additive to the existing fare model —
one more term in the same formula.

If 2.3 records the trail, waiting can be measured instead of typed in. An earlier
street-pickup meter did this: for each pair of GPS samples, if the speed between them is
below a floor (a few km/h), count the elapsed time as waiting minutes; otherwise count the
distance. Fare = km × per-km + waiting min × per-minute. Re-implement the idea. Do not copy
that code.

---

## Stage 3 — Make it nicer to use

### 3.1 Place search + reverse labels — **shipped**

See [geospatial.md §5](geospatial.md#5-geocoding-shipped). Pelias on the ORS key.
GebetaMaps or self-hosted Photon only if you later need better local names / independence.

### 3.2 Repeat a trip in one tap — **shipped**

History rows reload both pins onto the estimate map.

### 3.3 Quote sharing — **shipped**

The Share button sends one line ("Bole → Ayat, 14.0 km, ~35 min, ETB 240") plus a Google
Maps directions link for the same pins, through the phone's share sheet, or copies it where
there is no share sheet. The receipt idea below is
still open.

**Trigger:** a passenger asks for the price in writing, or you want to confirm a booking
in advance.

**Work:** render the estimate as a shareable line of text ("Bole → Ayat, 14 km, ~35 min,
ETB 240") via the browser's share sheet, into Telegram or SMS. A printable receipt for a
completed trip is the same idea, one step further.

### 3.4 Earnings view

**Trigger:** you want to know what you actually made last month.

**Work:** the totals line from v1's history screen, expanded: per-day and per-month
charged, kilometres driven, and — if you enter fuel costs — a rough profit number.
Deliberately last, because it's the most tempting thing to build first and the least
useful without the months of data that stages 1 and 2 produce.

---

## Stage 4 — More than one driver

**Trigger: a second person needs to use it.** Not "might one day" — actually does.

Be honest that this is a different product, and that most of the v1 decisions were correct
*because* there was one user. Crossing this line means:

- accounts and authentication (real ones, not a shared token)
- Postgres instead of SQLite, once there are concurrent writers
- per-user rate cards and per-user trip isolation
- a hosted deployment with the operational responsibilities that follow

At that point it's worth re-reading [background.md](background.md), because the earlier
ride-hailing backend already solved authentication, RBAC and multi-user driver records —
and that would finally be the right context in which to borrow from it.

**What would still not follow:** matching, dispatch, a passenger app, or in-app payments.
Several drivers each quoting their own trips is a shared tool. A marketplace is a company.
Don't let stage 4 quietly become that by accident.

---

## Deliberately not planned

- **MapLibre Native / Flutter / React Native / Swift / Kotlin apps** — keep MapLibre GL
  JS + the web UI. Native map SDKs only matter if you later decide to ship a store app with
  offline MBTiles; they do not improve ORS routes, fares, or place labels in the current
  web UI. Revisit only with an explicit App Store / Play Store goal.
- Real-time passenger tracking
- Automatic surge pricing
- Anything requiring a payment provider integration
- Multi-tenant SaaS
