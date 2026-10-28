package main

import (
	"path/filepath"
	"testing"
)

func TestStoreTripsAndPlaces(t *testing.T) {
	s, err := OpenStore(filepath.Join(t.TempDir(), "t.db"), schemaSQL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	charged := 120.0
	trip := &Trip{
		PickupLat: 9.01, PickupLng: 38.76,
		DestLat: 9.02, DestLng: 38.77,
		DistanceKM: 3.5, DurationMin: 12,
		SuggestedFare: 100, ActualCharged: &charged, Multiplier: 1,
		RateBase: 50, RatePerKM: 15, RatePerMin: 1,
	}
	if err := s.InsertTrip(trip); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListTrips()
	if err != nil || len(list) != 1 || list[0].ActualCharged == nil {
		t.Fatalf("list trips: %#v err=%v", list, err)
	}
	note := "n"
	if ok, err := s.UpdateTrip(trip.ID, nil, &note); err != nil || !ok {
		t.Fatalf("update: ok=%v err=%v", ok, err)
	}
	if ok, err := s.DeleteTrip(trip.ID); err != nil || !ok {
		t.Fatalf("delete trip: ok=%v err=%v", ok, err)
	}

	p := &Place{Label: "Bole", Lat: 9.01, Lng: 38.76}
	if err := s.InsertPlace(p); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertPlace(&Place{Label: "Bole", Lat: 9.02, Lng: 38.77}); err == nil {
		t.Fatal("expected UNIQUE on place label")
	}
	if ok, err := s.DeletePlace(p.ID); err != nil || !ok {
		t.Fatalf("delete place: ok=%v err=%v", ok, err)
	}
}

func TestStoreTripStopAndWaiting(t *testing.T) {
	s, err := OpenStore(filepath.Join(t.TempDir(), "t.db"), schemaSQL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	lat, lng := 9.015, 38.765
	withStop := &Trip{
		PickupLat: 9.01, PickupLng: 38.76, DestLat: 9.02, DestLng: 38.77,
		StopLabel: "Megenagna", StopLat: &lat, StopLng: &lng, WaitingMin: 12,
		DistanceKM: 4, DurationMin: 15, SuggestedFare: 150, Multiplier: 1,
		RateBase: 50, RatePerKM: 15, RatePerMin: 1, RatePerWaitMin: 3,
	}
	plain := &Trip{
		PickupLat: 9.01, PickupLng: 38.76, DestLat: 9.02, DestLng: 38.77,
		DistanceKM: 3.5, DurationMin: 12, SuggestedFare: 100, Multiplier: 1,
		RateBase: 50, RatePerKM: 15, RatePerMin: 1,
	}
	for _, tr := range []*Trip{withStop, plain} {
		if err := s.InsertTrip(tr); err != nil {
			t.Fatal(err)
		}
	}
	list, err := s.ListTrips()
	if err != nil || len(list) != 2 {
		t.Fatalf("list: %#v err=%v", list, err)
	}
	byID := map[int64]Trip{list[0].ID: list[0], list[1].ID: list[1]}

	got := byID[withStop.ID]
	if got.StopLat == nil || *got.StopLat != lat || *got.StopLng != lng ||
		got.StopLabel != "Megenagna" || got.WaitingMin != 12 || got.RatePerWaitMin != 3 {
		t.Fatalf("stop trip: %#v", got)
	}
	if got := byID[plain.ID]; got.StopLat != nil || got.StopLng != nil || got.WaitingMin != 0 {
		t.Fatalf("plain trip: %#v", got)
	}
}
