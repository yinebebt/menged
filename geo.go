package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Point is a WGS84 coordinate.
type Point struct {
	Lat float64 `json:"lat"`
	Lng float64 `json:"lng"`
}

// Route is a driving path between two points.
type Route struct {
	DistanceKM  float64 `json:"distance_km"`
	DurationMin float64 `json:"duration_min"`
	Geometry    string  `json:"geometry"` // encoded polyline, precision 5
}

// PlaceHit is one autocomplete / search result.
type PlaceHit struct {
	Label string  `json:"label"`
	Lat   float64 `json:"lat"`
	Lng   float64 `json:"lng"`
}

// Router computes a driving route.
type Router interface {
	Route(ctx context.Context, from, to Point) (Route, error)
}

// Geocoder resolves place names and reverse-labels coordinates.
type Geocoder interface {
	Autocomplete(ctx context.Context, q string, near Point) ([]PlaceHit, error)
	Reverse(ctx context.Context, p Point) (string, error)
}

// NewRouter returns a Router for cfg.Routing.Provider.
func NewRouter(cfg *Config) (Router, error) {
	switch cfg.Routing.Provider {
	case "ors", "openrouteservice":
		return newORSRouter(cfg)
	case "osrm":
		return newOSRMRouter(cfg)
	default:
		return nil, fmt.Errorf("unknown routing provider %q", cfg.Routing.Provider)
	}
}

// NewGeocoder builds geocoding from config (Pelias via the ORS key).
// Returns nil when geocoding is off or the key is missing.
func NewGeocoder(cfg *Config) Geocoder {
	provider := strings.ToLower(strings.TrimSpace(cfg.Geocoding.Provider))
	if provider == "" {
		provider = "pelias"
	}
	if provider == "off" {
		return nil
	}
	g := newPeliasGeocoder(cfg)
	if g == nil {
		return nil
	}
	return &cachedGeocoder{inner: g, rev: map[string]revHit{}}
}

// permanentRouteError is not worth retrying (bad pins, auth, etc.).
type permanentRouteError struct{ err error }

func (e permanentRouteError) Error() string { return e.err.Error() }
func (e permanentRouteError) Unwrap() error { return e.err }

func isRetryableRoute(err error) bool {
	var perm permanentRouteError
	return err != nil && !errors.As(err, &perm)
}

func httpClient() *http.Client {
	return &http.Client{Timeout: 5 * time.Second}
}

const revCacheTTL = 30 * time.Minute

// cachedGeocoder wraps a Geocoder and memoizes Reverse by ~50 m cell.
type cachedGeocoder struct {
	inner Geocoder
	mu    sync.Mutex
	rev   map[string]revHit
}

type revHit struct {
	label string
	at    time.Time
}

// reverseCacheKey rounds to ~50 m so nearby pin nudges share one entry.
func reverseCacheKey(p Point) string {
	const step = 0.00045
	lat := math.Round(p.Lat/step) * step
	lng := math.Round(p.Lng/step) * step
	return fmt.Sprintf("%.5f,%.5f", lat, lng)
}

func (c *cachedGeocoder) Autocomplete(ctx context.Context, q string, near Point) ([]PlaceHit, error) {
	return c.inner.Autocomplete(ctx, q, near)
}

func (c *cachedGeocoder) Reverse(ctx context.Context, p Point) (string, error) {
	key := reverseCacheKey(p)

	c.mu.Lock()
	if hit, ok := c.rev[key]; ok && time.Since(hit.at) < revCacheTTL {
		c.mu.Unlock()
		return hit.label, nil
	}
	c.mu.Unlock()

	label, err := c.inner.Reverse(ctx, p)
	if err != nil {
		return "", err
	}

	c.mu.Lock()
	c.rev[key] = revHit{label: label, at: time.Now()}
	c.mu.Unlock()
	return label, nil
}
