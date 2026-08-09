package main

import "testing"

func TestReverseCacheKeyBucketsNearbyPoints(t *testing.T) {
	a := Point{Lat: 9.01000, Lng: 38.76000}
	b := Point{Lat: 9.01002, Lng: 38.76003} // ~few metres
	c := Point{Lat: 9.02000, Lng: 38.77000} // far

	if reverseCacheKey(a) != reverseCacheKey(b) {
		t.Fatalf("nearby points should share key: %q vs %q", reverseCacheKey(a), reverseCacheKey(b))
	}
	if reverseCacheKey(a) == reverseCacheKey(c) {
		t.Fatalf("distant points should not share key")
	}
}

func TestPeliasHitsPreferLabel(t *testing.T) {
	hits := peliasHits([]peliasFeature{{
		Properties: struct {
			Label string `json:"label"`
			Name  string `json:"name"`
		}{Label: "Bole Road, Addis Ababa, Ethiopia", Name: "Bole Road"},
		Geometry: struct {
			Coordinates []float64 `json:"coordinates"`
		}{Coordinates: []float64{38.76, 9.01}},
	}})
	if len(hits) != 1 || hits[0].Label != "Bole Road, Addis Ababa, Ethiopia" {
		t.Fatalf("got %#v", hits)
	}
}
