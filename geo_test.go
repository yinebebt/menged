package main

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
)

func TestInServiceArea(t *testing.T) {
	if !InServiceArea(Point{Lat: 9.01, Lng: 38.76}) {
		t.Fatal("Addis should be inside")
	}
	if InServiceArea(Point{Lat: 0, Lng: 0}) {
		t.Fatal("null island should be outside")
	}
}

func TestIsORSUnroutable(t *testing.T) {
	if !isORSUnroutable([]byte(`{"error":{"code":2010,"message":"Could not find routable point"}}`)) {
		t.Fatal("want unroutable")
	}
	if isORSUnroutable([]byte(`{"error":{"code":2004}}`)) {
		t.Fatal("want routable/other")
	}
}

func TestCachedRouter(t *testing.T) {
	var calls atomic.Int32
	inner := RouterFunc(func(ctx context.Context, from, to Point) (Route, error) {
		n := calls.Add(1)
		if n == 1 {
			return Route{}, errors.New("transient")
		}
		return Route{DistanceKM: 1.5}, nil
	})
	r := &cachedRouter{inner: inner, cache: map[string]cachedRoute{}}
	from, to := Point{Lat: 9.01, Lng: 38.76}, Point{Lat: 9.02, Lng: 38.77}

	got, err := r.Route(context.Background(), from, to)
	if err != nil || got.DistanceKM != 1.5 || calls.Load() != 2 {
		t.Fatalf("retry: got=%#v err=%v calls=%d", got, err, calls.Load())
	}
	if _, err := r.Route(context.Background(), from, to); err != nil || calls.Load() != 2 {
		t.Fatalf("cache: err=%v calls=%d", err, calls.Load())
	}

	perm := &cachedRouter{
		inner: RouterFunc(func(ctx context.Context, from, to Point) (Route, error) {
			calls.Add(1)
			return Route{}, permanentRouteError{ErrUnroutable}
		}),
		cache: map[string]cachedRoute{},
	}
	before := calls.Load()
	_, err = perm.Route(context.Background(), from, to)
	if !errors.Is(err, ErrUnroutable) || calls.Load() != before+1 {
		t.Fatalf("permanent: err=%v calls=%d", err, calls.Load()-before)
	}
}

func TestReverseCacheKey(t *testing.T) {
	a := Point{Lat: 9.01000, Lng: 38.76000}
	b := Point{Lat: 9.01002, Lng: 38.76003}
	c := Point{Lat: 9.02000, Lng: 38.77000}
	if reverseCacheKey(a) != reverseCacheKey(b) {
		t.Fatal("nearby points should share key")
	}
	if reverseCacheKey(a) == reverseCacheKey(c) {
		t.Fatal("distant points should not share key")
	}
}

func TestPeliasHits(t *testing.T) {
	hits := peliasHits([]peliasFeature{{
		Properties: struct {
			Label string `json:"label"`
			Name  string `json:"name"`
		}{Label: "Bole Road, Addis Ababa", Name: "Bole Road"},
		Geometry: struct {
			Coordinates []float64 `json:"coordinates"`
		}{Coordinates: []float64{38.76, 9.01}},
	}})
	if len(hits) != 1 || hits[0].Label != "Bole Road, Addis Ababa" {
		t.Fatalf("got %#v", hits)
	}
}

type RouterFunc func(ctx context.Context, from, to Point) (Route, error)

func (f RouterFunc) Route(ctx context.Context, from, to Point) (Route, error) {
	return f(ctx, from, to)
}
