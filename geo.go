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

const (
	minLat, maxLat = 3.0, 15.5 // Ethiopia-ish bbox
	minLng, maxLng = 32.5, 48.5
)

var (
	ErrOutsideArea = errors.New("point outside the service area")
	ErrUnroutable  = errors.New("no road near that pin — move it closer to a street")
)

func InServiceArea(p Point) bool {
	return p.Lat >= minLat && p.Lat <= maxLat && p.Lng >= minLng && p.Lng <= maxLng
}

func NewRouter(cfg *Config) (Router, error) {
	var inner Router
	var err error
	switch cfg.Routing.Provider {
	case "ors", "openrouteservice":
		inner, err = newORSRouter(cfg)
	case "osrm":
		inner, err = newOSRMRouter(cfg)
	default:
		return nil, fmt.Errorf("unknown routing provider %q", cfg.Routing.Provider)
	}
	if err != nil {
		return nil, err
	}
	return &cachedRouter{inner: inner, cache: map[string]cachedRoute{}}, nil
}

func NewGeocoder(cfg *Config) (Geocoder, error) {
	provider := strings.ToLower(strings.TrimSpace(cfg.Geocoding.Provider))
	if provider == "" {
		provider = "pelias"
	}
	switch provider {
	case "off":
		return nil, nil
	case "pelias":
		g := newPeliasGeocoder(cfg)
		if g == nil {
			return nil, nil
		}
		return &cachedGeocoder{inner: g, rev: map[string]revHit{}}, nil
	default:
		return nil, fmt.Errorf("unknown geocoding provider %q", provider)
	}
}

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

const (
	routeCacheTTL = 5 * time.Minute
	revCacheTTL   = 30 * time.Minute
)

type cachedRouter struct {
	inner Router
	mu    sync.Mutex
	cache map[string]cachedRoute
}

type cachedRoute struct {
	route Route
	at    time.Time
}

func (c *cachedRouter) Route(ctx context.Context, from, to Point) (Route, error) {
	key := fmt.Sprintf("%.4f,%.4f|%.4f,%.4f", from.Lat, from.Lng, to.Lat, to.Lng)

	c.mu.Lock()
	if hit, ok := c.cache[key]; ok && time.Since(hit.at) < routeCacheTTL {
		c.mu.Unlock()
		return hit.route, nil
	}
	c.mu.Unlock()

	var route Route
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		route, err = c.inner.Route(ctx, from, to)
		if err == nil || ctx.Err() != nil || !isRetryableRoute(err) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		return Route{}, err
	}

	c.mu.Lock()
	if len(c.cache) > 500 {
		clear(c.cache)
	}
	c.cache[key] = cachedRoute{route: route, at: time.Now()}
	c.mu.Unlock()
	return route, nil
}

type cachedGeocoder struct {
	inner Geocoder
	mu    sync.Mutex
	rev   map[string]revHit
}

type revHit struct {
	label string
	at    time.Time
}

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
