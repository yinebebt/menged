# Fares

Every fare menged shows comes from one function, `CalculateFare` in `fare.go`, and the
rate card in the `fare` section of the config. The server does the arithmetic, and the
client only displays the result.

## The formula

```
raw  = base_fare
     + distance_km × price_per_km
     + duration_min × price_per_minute
     + waiting_min × price_per_waiting_minute

fare = max(raw, minimum_fare) × multiplier, rounded to the nearest `rounding`
```

- **Distance** is road distance from the router, never a straight line.
- **Duration** is driving time. It is there because the same 6 km through Bole is a
  different job at 6 PM than at 6 AM. Set `price_per_minute: 0` to price by distance
  only.
- **Waiting** is time stopped: waiting for the passenger, or at a stop. For a quote the
  driver types it in. For a recorded trip it is measured (below). Setting
  `price_per_waiting_minute: 0` hides the field.
- **Multiplier** is chosen by hand in the app (×1, ×1.25, ×1.5) for night, rain or
  traffic. Nothing raises it automatically.

Each response includes a breakdown with every term, whether the minimum fare applied,
and the rates used. When a trip is saved, those rates are stored with it.

## Recorded trips

A recorded trip is priced with the same formula. The inputs are measured from the GPS
fixes the browser collected (`measureTrack` in `track.go`):

1. **Clean the track.** Fixes outside the service area, or not newer than the previous
   one, are dropped. Fixes less accurate than 50 m are dropped too, unless no better
   ones exist. That happens with Wi-Fi location on a laptop or a phone indoors. The trip
   is then marked `rough`.
2. **Ignore jitter.** Starting from the first fix, a fix only counts as a move if it is
   further than 20 m, and further than its own reported accuracy, from the last counted
   fix. A parked phone drifting around does not add distance.
3. **Split the time.** Each counted move is either driving or waiting, depending on its
   average speed. Below 5 km/h it is waiting time, so a stop or crawling traffic between
   two moves is priced as waiting. Time after the last move is not counted: that is the
   driver arriving and pressing End.
4. **Get the road distance.** Points about 300 m apart along the counted path (at most
   25) are sent to the router as waypoints, and its road distance replaces the GPS
   distance. This follows curves that straight GPS hops would cut. If routing fails, the
   GPS distance is used and the result says `method: "gps"`.

The live meter during a trip skips step 4, so it doesn't spend routing quota every 30
seconds. The final price comes from the call made at "End trip".

**Known limits.** A sampled point that lands on the wrong side of a divided road can add
a small detour. A simulated 14 km drive came out about 3% long. Snapping the track to
roads (OSRM's `match` service) would remove that and is on the
[roadmap](roadmap.md). Recording stops if the page goes to the background, because
browsers don't give web pages GPS there.
