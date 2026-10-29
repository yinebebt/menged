# Roadmap

menged does what one driver needs today: quotes, recorded trips, and a trip log. The
items below are possible next steps. Each one names the situation that would make it
worth building, because most of them only pay off once there is real usage behind them.
None of them needs a redesign; they fit into the current structure.

## Reliability

**Self-hosted OSRM.** A `docker-compose.yml` running `osrm-routed` on the Ethiopia
extract, plus a script that downloads and preprocesses it. The `Router` interface and
the `osrm` provider already exist, so this is packaging, not code. It removes the
openrouteservice quota and, on the same machine, makes quotes work offline.
*Worth it when* the free quota runs out, the hosted API is slow or down, or you want no
third party in the loop.

**Snap-to-road for recorded trips.** Use OSRM's `match` service to fit the GPS track to
the road network instead of routing through sampled points. That removes the small
detours a sample on the wrong side of a divided road can add (see [fares.md](fares.md)).
*Worth it when* real recorded trips come out noticeably longer than the odometer says.

**Offline app shell.** A service worker that caches the app (network-first, so updates
aren't stuck), and queues saves made without a connection. Quotes still need a router,
so this mainly helps trip recording and the log.
*Worth it when* the app has opened to a blank page in a dead zone.

## Better numbers

**Fare calibration.** Every saved trip already stores the suggested fare, what was
charged, and the rates used. A report over that history could say "your fares were 12%
below what you charged on average; a per-km rate of 13.4 would have matched". This is
the most useful item here, and the data for it is already being collected.
*Worth it when* there are about 50 trips with `actual_charged` filled in.

**Duration by time of day.** No free router has live Addis Ababa traffic. Recorded trips
have real start and end times, so a correction factor per hour of the day (and per
corridor, eventually) can be learned from them.
*Worth it when* quoted durations are visibly optimistic at rush hour.

## Nicer to use

**Receipts.** A printable or shareable summary of a completed trip, the same idea as
quote sharing one step further.
*Worth it when* passengers ask for one.

**Earnings view.** Per-day and per-month totals of charged amounts and kilometres, and,
with fuel costs entered, a rough profit figure.
*Worth it when* there are a few months of trips to look at. Before that it is just an
empty chart.

## More than one driver

Accounts and per-user trips are already in. A shared deployment for several drivers
would also need:

- rate cards per driver, instead of one rate card in the config;
- user management (listing and removing accounts) without a shell on the server;
- Postgres instead of SQLite, once enough drivers write at the same time for it to
  matter.

*Worth it when* a second driver actually uses it, not before.

## Out of scope

These would turn a driver's tool into a platform, and aren't planned:

- matching passengers with drivers, dispatch, live passenger tracking;
- a passenger app, in-app payments, automatic surge pricing;
- native mobile apps. The web app with MapLibre GL JS covers the use case. A store app
  would only make sense together with offline map tiles.
