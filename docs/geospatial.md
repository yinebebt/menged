# Maps, routing and geocoding

menged uses three outside services: map tiles for the display, a router for driving
distance and time, and a geocoder for place search and pin labels. All three have free
options that cover a driver's volume (tens of quotes a day). Free-tier limits below were
checked in August 2026; check them again before relying on them.

| Need | Default | Alternative |
|------|---------|-------------|
| Map display | MapLibre GL JS with OpenFreeMap tiles | Any MapLibre style URL |
| Driving route | openrouteservice (HeiGIT) | Self-hosted OSRM |
| Place search, pin labels | Pelias on the openrouteservice key | Off |

## Map display

The web app draws the map with [MapLibre GL JS](https://maplibre.org/) and the
[OpenFreeMap](https://openfreemap.org/) "liberty" style, which needs no API key. The
style URL is `server.map_style`. The starting camera and the pan limits (Greater Addis
Ababa) come from `/api/config`.

Mapbox and Google Maps would need an account and a key in the page. Leaflet with raster
OSM tiles works but looks worse on a phone.

## Driving route

**openrouteservice** is the default (`routing.provider: ors`), at
`https://api.heigit.org/openrouteservice` with the key in `ORS_API_KEY`. The free plan
allows about 2,000 directions requests a day. menged asks for the `driving-car` profile
with a 5 km snap radius per point, so a pin dropped off the centre of a street still
routes. A pin with no road within reach returns a 422 ("no road near that pin") rather
than a generic error. The old `api.openrouteservice.org` host has been retired.

**OSRM** (`routing.provider: osrm`, with `routing.base_url` pointing at your
`osrm-routed`) removes the quota and the third party. It needs the
[Geofabrik Ethiopia extract](https://download.geofabrik.de/africa/ethiopia.html)
preprocessed and a small always-on machine. If that machine is the one menged runs on,
quotes work without internet. A ready-made setup for it is on the
[roadmap](roadmap.md).

Both return the route as a precision-5 encoded polyline, which the web app decodes and
draws.

## Place search and labels

Addis Ababa is navigated by landmark, and OpenStreetMap-based geocoders are uneven on
informal names. So **saved places** (name a pin once, set it with one tap later) are the
main way to set a pin you use often. Remote search is there to help.

**Pelias** on the same openrouteservice key (`geocoding.provider: pelias`, the default)
provides both:

- **Search** (`/api/geocode`) returns up to five places in Ethiopia, biased towards
  the map's position. In the app, saved places are listed above remote hits.
- **Reverse labels** (`/api/reverse`) name a pin set by a tap, a drag or GPS. Results are
  cached for about 30 minutes per 50 m, so nudging a pin is free.

The free plan allows about 3,000 geocoding requests a day. `geocoding.provider: off`
turns search and labels off; pins are then labelled only when they are saved places.

Other options looked at:

- [GebetaMaps](https://docs.gebeta.app/) has strong local data, but API access is
  gated. It isn't wired in.
- Self-hosted [Photon](https://github.com/komoot/photon) would remove the dependency on
  openrouteservice for search.
- The public Nominatim and Photon demo servers don't allow production traffic.

## Attribution

Keep MapLibre's OpenStreetMap attribution control on the map, and credit OpenFreeMap as
its terms ask. Route data from openrouteservice and OSRM is also OpenStreetMap-derived.
