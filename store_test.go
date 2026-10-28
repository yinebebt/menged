package main

import (
	"path/filepath"
	"testing"
)

// newTestStore opens a store in a temp dir with one user and returns that user's id.
func newTestStore(t *testing.T) (*Store, int64) {
	t.Helper()
	s, err := OpenStore(filepath.Join(t.TempDir(), "t.db"), schemaSQL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, addUser(t, s, "driver")
}

func addUser(t *testing.T, s *Store, name string) int64 {
	t.Helper()
	if _, err := s.SetPassword(name, "correct horse"); err != nil {
		t.Fatal(err)
	}
	u, err := s.Authenticate(name, "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	return u.ID
}

func TestStorePragmas(t *testing.T) {
	s, _ := newTestStore(t)
	var mode string
	var fk int
	if err := s.db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("journal_mode=%q err=%v", mode, err)
	}
	if err := s.db.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil || fk != 1 {
		t.Fatalf("foreign_keys=%d err=%v", fk, err)
	}
	orphan := &Trip{UserID: 999, PickupLat: 9.01, PickupLng: 38.76, DestLat: 9.02, DestLng: 38.77,
		DistanceKM: 1, SuggestedFare: 80, Multiplier: 1}
	if err := s.InsertTrip(orphan); err == nil {
		t.Fatal("a trip for a user that doesn't exist was accepted")
	}
}

func TestStoreTripsAndPlaces(t *testing.T) {
	s, uid := newTestStore(t)

	charged := 120.0
	trip := &Trip{
		UserID:    uid,
		PickupLat: 9.01, PickupLng: 38.76,
		DestLat: 9.02, DestLng: 38.77,
		DistanceKM: 3.5, DurationMin: 12,
		SuggestedFare: 100, ActualCharged: &charged, Multiplier: 1,
		RateBase: 50, RatePerKM: 15, RatePerMin: 1,
	}
	if err := s.InsertTrip(trip); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListTrips(uid)
	if err != nil || len(list) != 1 || list[0].ActualCharged == nil {
		t.Fatalf("list trips: %#v err=%v", list, err)
	}
	note := "n"
	if ok, err := s.UpdateTrip(uid, trip.ID, nil, &note); err != nil || !ok {
		t.Fatalf("update: ok=%v err=%v", ok, err)
	}
	if ok, err := s.DeleteTrip(uid, trip.ID); err != nil || !ok {
		t.Fatalf("delete trip: ok=%v err=%v", ok, err)
	}

	p := &Place{UserID: uid, Label: "Bole", Lat: 9.01, Lng: 38.76}
	if err := s.InsertPlace(p); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertPlace(&Place{UserID: uid, Label: "Bole", Lat: 9.02, Lng: 38.77}); err == nil {
		t.Fatal("expected UNIQUE on place label per user")
	}
	if ok, err := s.DeletePlace(uid, p.ID); err != nil || !ok {
		t.Fatalf("delete place: ok=%v err=%v", ok, err)
	}
}

func TestStoreTripStopAndWaiting(t *testing.T) {
	s, uid := newTestStore(t)

	lat, lng := 9.015, 38.765
	withStop := &Trip{
		UserID:    uid,
		PickupLat: 9.01, PickupLng: 38.76, DestLat: 9.02, DestLng: 38.77,
		StopLabel: "Megenagna", StopLat: &lat, StopLng: &lng, WaitingMin: 12,
		DistanceKM: 4, DurationMin: 15, SuggestedFare: 150, Multiplier: 1,
		RateBase: 50, RatePerKM: 15, RatePerMin: 1, RatePerWaitMin: 3,
	}
	plain := &Trip{
		UserID:    uid,
		PickupLat: 9.01, PickupLng: 38.76, DestLat: 9.02, DestLng: 38.77,
		DistanceKM: 3.5, DurationMin: 12, SuggestedFare: 100, Multiplier: 1,
		RateBase: 50, RatePerKM: 15, RatePerMin: 1,
	}
	for _, tr := range []*Trip{withStop, plain} {
		if err := s.InsertTrip(tr); err != nil {
			t.Fatal(err)
		}
	}
	list, err := s.ListTrips(uid)
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

func TestStoreRecordedTrip(t *testing.T) {
	s, uid := newTestStore(t)

	quoted := 240.0
	rec := &Trip{
		UserID:    uid,
		PickupLat: 9.01, PickupLng: 38.76, DestLat: 9.02, DestLng: 38.77,
		DistanceKM: 0, DurationMin: 0, SuggestedFare: 250, Multiplier: 1,
		RateBase: 50, RatePerKM: 15, RatePerMin: 1,
		StartedAt: "2026-10-28T10:00:00+03:00", EndedAt: "2026-10-28T10:20:00+03:00",
		Track: "_p~iF~ps|U", QuotedFare: &quoted,
	}
	quote := &Trip{
		UserID:    uid,
		PickupLat: 9.01, PickupLng: 38.76, DestLat: 9.02, DestLng: 38.77,
		DistanceKM: 3, DurationMin: 10, SuggestedFare: 250, Multiplier: 1,
		RateBase: 50, RatePerKM: 15, RatePerMin: 1,
	}
	for _, tr := range []*Trip{rec, quote} {
		if err := s.InsertTrip(tr); err != nil {
			t.Fatal(err)
		}
	}
	list, err := s.ListTrips(uid)
	if err != nil {
		t.Fatal(err)
	}
	for _, got := range list {
		switch got.ID {
		case rec.ID:
			if got.StartedAt != rec.StartedAt || got.EndedAt != rec.EndedAt || got.Track != rec.Track ||
				got.QuotedFare == nil || *got.QuotedFare != 240 {
				t.Fatalf("recorded trip: %#v", got)
			}
		case quote.ID:
			if got.StartedAt != "" || got.Track != "" || got.QuotedFare != nil {
				t.Fatalf("quote trip: %#v", got)
			}
		}
	}
}
