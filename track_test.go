package main

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const t0 = int64(1_790_000_000_000) // any fixed unix-ms start

// eastOf returns the point metres east of start, near Addis.
func eastOf(start Point, metres float64) Point {
	mPerDegLng := 111320 * math.Cos(start.Lat*math.Pi/180)
	return Point{Lat: start.Lat, Lng: start.Lng + metres/mPerDegLng}
}

var bole = Point{Lat: 8.9958, Lng: 38.7880}

// drive appends fixes every 5 s moving east at kmh for seconds.
func drive(track []TrackPoint, kmh float64, seconds int) []TrackPoint {
	last := track[len(track)-1]
	for s := 5; s <= seconds; s += 5 {
		p := eastOf(Point{Lat: last.Lat, Lng: last.Lng}, kmh/3.6*float64(s))
		track = append(track, TrackPoint{Lat: p.Lat, Lng: p.Lng, T: last.T + int64(s)*1000, Acc: 8})
	}
	return track
}

// stand appends fixes every 5 s jittering a few metres around the last position.
func stand(track []TrackPoint, seconds int) []TrackPoint {
	last := track[len(track)-1]
	jitter := []float64{6, -4, 9, -8, 3, -6}
	for i, s := 0, 5; s <= seconds; i, s = i+1, s+5 {
		p := eastOf(Point{Lat: last.Lat, Lng: last.Lng}, jitter[i%len(jitter)])
		track = append(track, TrackPoint{Lat: p.Lat, Lng: p.Lng, T: last.T + int64(s)*1000, Acc: 12})
	}
	return track
}

func start() []TrackPoint {
	return []TrackPoint{{Lat: bole.Lat, Lng: bole.Lng, T: t0, Acc: 8}}
}

func near(got, want, tol float64) bool { return math.Abs(got-want) <= tol }

func TestMeasureTrack(t *testing.T) {
	t.Run("standing still is not distance", func(t *testing.T) {
		m := measureTrack(stand(start(), 300))
		if m.GPSDistanceKM != 0 || m.MovingMin != 0 || m.WaitingMin != 0 || len(m.Path) != 1 {
			t.Fatalf("%+v", m)
		}
	})

	t.Run("steady driving", func(t *testing.T) {
		m := measureTrack(drive(start(), 30, 600)) // 10 min at 30 km/h = 5 km
		if !near(m.GPSDistanceKM, 5, 0.01) || !near(m.MovingMin, 10, 0.01) || m.WaitingMin != 0 {
			t.Fatalf("%+v", m)
		}
	})

	t.Run("a stop in the middle is waiting", func(t *testing.T) {
		tr := drive(start(), 30, 300)
		tr = stand(tr, 240)
		tr = drive(tr, 30, 300)
		m := measureTrack(tr)
		// 5 km driven; the 4 min stop shows up as waiting (plus the jitter of the stop
		// itself, a few tens of metres).
		if !near(m.GPSDistanceKM, 5, 0.05) || !near(m.WaitingMin, 4, 0.2) || !near(m.MovingMin, 10, 0.2) {
			t.Fatalf("%+v", m)
		}
	})

	t.Run("time after the last move is not waiting", func(t *testing.T) {
		m := measureTrack(stand(drive(start(), 30, 300), 300))
		if m.WaitingMin > 0.1 || !near(m.MovingMin, 5, 0.01) {
			t.Fatalf("%+v", m)
		}
	})

	t.Run("bad fixes are dropped", func(t *testing.T) {
		tr := drive(start(), 30, 300)
		far := eastOf(bole, 3000)
		tr = append(tr,
			TrackPoint{Lat: far.Lat, Lng: far.Lng, T: tr[len(tr)-1].T + 1000, Acc: 400}, // inaccurate
			TrackPoint{Lat: 0, Lng: 0, T: tr[len(tr)-1].T + 2000},                       // outside area
			TrackPoint{Lat: far.Lat, Lng: far.Lng, T: tr[0].T},                          // time went backwards
		)
		if m := measureTrack(tr); !near(m.GPSDistanceKM, 2.5, 0.01) {
			t.Fatalf("%+v", m)
		}
	})

	t.Run("only rough fixes: still measured, jitter ignored", func(t *testing.T) {
		// Laptop Wi-Fi location: ±150 m, wandering up to 120 m while not moving.
		tr := []TrackPoint{{Lat: bole.Lat, Lng: bole.Lng, T: t0, Acc: 150}}
		for i, d := range []float64{80, -60, 120, -100, 40} {
			p := eastOf(bole, d)
			tr = append(tr, TrackPoint{Lat: p.Lat, Lng: p.Lng, T: t0 + int64(i+1)*30000, Acc: 150})
		}
		m := measureTrack(tr)
		if !m.Rough || len(m.Path) != 1 || m.GPSDistanceKM != 0 {
			t.Fatalf("%+v", m)
		}
	})

	t.Run("accurate fixes win over rough ones", func(t *testing.T) {
		tr := drive(start(), 30, 300)
		far := eastOf(bole, 3000)
		tr = append(tr, TrackPoint{Lat: far.Lat, Lng: far.Lng, T: tr[len(tr)-1].T + 5000, Acc: 400})
		if m := measureTrack(tr); m.Rough || !near(m.GPSDistanceKM, 2.5, 0.01) {
			t.Fatalf("%+v", m)
		}
	})

	t.Run("no usable fixes", func(t *testing.T) {
		if m := measureTrack([]TrackPoint{{Lat: 0, Lng: 0, T: t0}}); len(m.Path) != 0 {
			t.Fatalf("%+v", m)
		}
	})
}

func TestRouteSamples(t *testing.T) {
	path := measureTrack(drive(start(), 30, 600)).Path // ~5 km, a point every ~42 m
	s := routeSamples(path)
	if s[0] != path[0] || s[len(s)-1] != path[len(path)-1] || len(s) < 15 || len(s) > maxRoutePoints {
		t.Fatalf("%d samples from %d points", len(s), len(path))
	}
	long := measureTrack(drive(start(), 60, 3600)).Path // 60 km
	if n := len(routeSamples(long)); n != maxRoutePoints {
		t.Fatalf("long trip: %d samples", n)
	}
}

func TestEncodePolyline(t *testing.T) {
	// The example from Google's polyline documentation.
	got := encodePolyline([]Point{{38.5, -120.2}, {40.7, -120.95}, {43.252, -126.453}})
	if want := "_p~iF~ps|U_ulLnnqC_mqNvxq`@"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestMeasureHandler(t *testing.T) {
	cfg := &Config{Fare: FareConfig{
		Currency: "ETB", BaseFare: 50, PricePerKM: 10, PricePerMinute: 2,
		PricePerWait: 3, MinimumFare: 80, Rounding: 5,
	}}
	tr := drive(start(), 30, 300)
	tr = stand(tr, 240)
	tr = drive(tr, 30, 300)
	body, _ := json.Marshal(measureRequest{Track: tr, Route: true})

	post := func(router Router, body string) (*httptest.ResponseRecorder, measureResponse) {
		h := NewServer(cfg, nil, router, nil, logMailer{}).routes(http.NotFoundHandler())
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/measure", strings.NewReader(body)))
		var out measureResponse
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec, out
	}

	var routed []Point
	road := RouterFunc(func(ctx context.Context, pts []Point) (Route, error) {
		routed = pts
		return Route{DistanceKM: 5.4, Geometry: "road"}, nil
	})
	rec, out := post(road, string(body))
	if rec.Code != http.StatusOK || out.Method != "road" || out.DistanceKM != 5.4 || len(routed) < 2 {
		t.Fatalf("road: %d %+v", rec.Code, out)
	}
	// 50 + 5.4×10 + ~10 min×2 + ~4 min×3, rounded to 5
	if out.Fare < 130 || out.Fare > 140 {
		t.Fatalf("road fare %v (%+v)", out.Fare, out.Breakdown)
	}

	down := RouterFunc(func(ctx context.Context, pts []Point) (Route, error) {
		return Route{}, errors.New("router down")
	})
	if rec, out := post(down, string(body)); rec.Code != http.StatusOK || out.Method != "gps" || !near(out.DistanceKM, 5, 0.05) {
		t.Fatalf("fallback: %d %+v", rec.Code, out)
	}

	calls := 0
	counting := RouterFunc(func(ctx context.Context, pts []Point) (Route, error) {
		calls++
		return Route{}, nil
	})
	live, _ := json.Marshal(measureRequest{Track: tr})
	if rec, out := post(counting, string(live)); rec.Code != http.StatusOK || calls != 0 || out.Method != "gps" {
		t.Fatalf("live meter must not route: %d calls=%d %+v", rec.Code, calls, out)
	}

	if rec, _ := post(road, `{"track":[{"lat":0,"lng":0,"t":1}]}`); rec.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(rec.Body.String(), "outside the service area") {
		t.Fatalf("outside area: %d %s", rec.Code, rec.Body)
	}
	if rec, _ := post(road, `{"track":[]}`); rec.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(rec.Body.String(), "no GPS fixes recorded yet") {
		t.Fatalf("empty track: %d %s", rec.Code, rec.Body)
	}
	rough := `{"track":[{"lat":9.01,"lng":38.76,"t":1000,"acc":900},{"lat":9.011,"lng":38.761,"t":31000,"acc":900}]}`
	if rec, out := post(road, rough); rec.Code != http.StatusOK || !out.Rough || out.Fare != 80 {
		t.Fatalf("rough: %d %+v", rec.Code, out)
	}
}
