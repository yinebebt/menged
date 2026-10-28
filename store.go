package main

import (
	"database/sql"
	"time"

	_ "modernc.org/sqlite"
)

// Trip is a saved estimate plus the amount actually charged.
type Trip struct {
	ID        int64  `json:"id"`
	CreatedAt string `json:"created_at"`

	PickupLabel string  `json:"pickup_label,omitempty"`
	PickupLat   float64 `json:"pickup_lat"`
	PickupLng   float64 `json:"pickup_lng"`
	DestLabel   string  `json:"dest_label,omitempty"`
	DestLat     float64 `json:"dest_lat"`
	DestLng     float64 `json:"dest_lng"`

	StopLabel  string   `json:"stop_label,omitempty"`
	StopLat    *float64 `json:"stop_lat,omitempty"` // nil when the trip has no stop
	StopLng    *float64 `json:"stop_lng,omitempty"`
	WaitingMin float64  `json:"waiting_min"`

	DistanceKM    float64 `json:"distance_km"`
	DurationMin   float64 `json:"duration_min"`
	RouteGeometry string  `json:"route_geometry,omitempty"`

	SuggestedFare float64  `json:"suggested_fare"`
	ActualCharged *float64 `json:"actual_charged"` // nil when not recorded
	Multiplier    float64  `json:"multiplier"`

	RateBase       float64 `json:"rate_base"`
	RatePerKM      float64 `json:"rate_per_km"`
	RatePerMin     float64 `json:"rate_per_min"`
	RatePerWaitMin float64 `json:"rate_per_wait_min"`

	Note string `json:"note,omitempty"`

	// Set on recorded trips only. Distance, duration, waiting and suggested fare are
	// then the measured values, and QuotedFare is the quote given before starting.
	StartedAt  string   `json:"started_at,omitempty"`
	EndedAt    string   `json:"ended_at,omitempty"`
	Track      string   `json:"track,omitempty"` // polyline of the GPS fixes that counted
	QuotedFare *float64 `json:"quoted_fare,omitempty"`
}

// Place is a named landmark for one-tap pin setting.
type Place struct {
	ID        int64   `json:"id"`
	Label     string  `json:"label"`
	Lat       float64 `json:"lat"`
	Lng       float64 `json:"lng"`
	CreatedAt string  `json:"created_at"`
}

// Store is the SQLite persistence layer.
type Store struct {
	db *sql.DB
}

// OpenStore opens path, enables WAL, and applies schema.
func OpenStore(path, schema string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	// Single connection: one writer, avoids SQLITE_BUSY for this workload.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.Exec(`PRAGMA busy_timeout=5000`); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

func now() string { return time.Now().Format(time.RFC3339) }

const tripCols = `id, created_at, pickup_label, pickup_lat, pickup_lng,
	dest_label, dest_lat, dest_lng, stop_label, stop_lat, stop_lng, waiting_min,
	distance_km, duration_min, route_geometry, suggested_fare, actual_charged, multiplier,
	rate_base, rate_per_km, rate_per_min, rate_per_wait_min, note,
	started_at, ended_at, track, quoted_fare`

func scanTrip(row interface{ Scan(...any) error }) (Trip, error) {
	var t Trip
	var pickupLabel, destLabel, stopLabel, geometry, note sql.NullString
	var startedAt, endedAt, track sql.NullString
	var charged, stopLat, stopLng, quoted sql.NullFloat64
	err := row.Scan(&t.ID, &t.CreatedAt, &pickupLabel, &t.PickupLat, &t.PickupLng,
		&destLabel, &t.DestLat, &t.DestLng, &stopLabel, &stopLat, &stopLng, &t.WaitingMin,
		&t.DistanceKM, &t.DurationMin, &geometry, &t.SuggestedFare, &charged, &t.Multiplier,
		&t.RateBase, &t.RatePerKM, &t.RatePerMin, &t.RatePerWaitMin, &note,
		&startedAt, &endedAt, &track, &quoted)
	if err != nil {
		return t, err
	}
	t.PickupLabel = pickupLabel.String
	t.DestLabel = destLabel.String
	t.StopLabel = stopLabel.String
	if stopLat.Valid && stopLng.Valid {
		t.StopLat, t.StopLng = &stopLat.Float64, &stopLng.Float64
	}
	t.RouteGeometry = geometry.String
	t.Note = note.String
	t.StartedAt, t.EndedAt, t.Track = startedAt.String, endedAt.String, track.String
	if quoted.Valid {
		t.QuotedFare = &quoted.Float64
	}
	if charged.Valid {
		t.ActualCharged = &charged.Float64
	}
	return t, nil
}

// ListTrips returns trips newest first.
func (s *Store) ListTrips() ([]Trip, error) {
	rows, err := s.db.Query(`SELECT ` + tripCols + ` FROM trips ORDER BY created_at DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	trips := []Trip{}
	for rows.Next() {
		t, err := scanTrip(rows)
		if err != nil {
			return nil, err
		}
		trips = append(trips, t)
	}
	return trips, rows.Err()
}

// InsertTrip persists t and sets t.ID and t.CreatedAt.
func (s *Store) InsertTrip(t *Trip) error {
	t.CreatedAt = now()
	res, err := s.db.Exec(`INSERT INTO trips (created_at, pickup_label, pickup_lat, pickup_lng,
		dest_label, dest_lat, dest_lng, stop_label, stop_lat, stop_lng, waiting_min,
		distance_km, duration_min, route_geometry, suggested_fare, actual_charged, multiplier,
		rate_base, rate_per_km, rate_per_min, rate_per_wait_min, note,
		started_at, ended_at, track, quoted_fare)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		t.CreatedAt, t.PickupLabel, t.PickupLat, t.PickupLng,
		t.DestLabel, t.DestLat, t.DestLng, t.StopLabel, t.StopLat, t.StopLng, t.WaitingMin,
		t.DistanceKM, t.DurationMin, t.RouteGeometry, t.SuggestedFare, t.ActualCharged, t.Multiplier,
		t.RateBase, t.RatePerKM, t.RatePerMin, t.RatePerWaitMin, t.Note,
		nullIfEmpty(t.StartedAt), nullIfEmpty(t.EndedAt), nullIfEmpty(t.Track), t.QuotedFare)
	if err != nil {
		return err
	}
	t.ID, err = res.LastInsertId()
	return err
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// UpdateTrip sets actual_charged and/or note. Returns false if id is missing.
func (s *Store) UpdateTrip(id int64, charged *float64, note *string) (bool, error) {
	set, args := "", []any{}
	if charged != nil {
		set += "actual_charged = ?"
		args = append(args, *charged)
	}
	if note != nil {
		if set != "" {
			set += ", "
		}
		set += "note = ?"
		args = append(args, *note)
	}
	if set == "" {
		return false, nil
	}
	args = append(args, id)
	res, err := s.db.Exec("UPDATE trips SET "+set+" WHERE id = ?", args...)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// DeleteTrip removes a trip by id. Returns false if id is missing.
func (s *Store) DeleteTrip(id int64) (bool, error) {
	res, err := s.db.Exec(`DELETE FROM trips WHERE id = ?`, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// ListPlaces returns saved places sorted by label.
func (s *Store) ListPlaces() ([]Place, error) {
	rows, err := s.db.Query(`SELECT id, label, lat, lng, created_at FROM places ORDER BY label`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	places := []Place{}
	for rows.Next() {
		var p Place
		if err := rows.Scan(&p.ID, &p.Label, &p.Lat, &p.Lng, &p.CreatedAt); err != nil {
			return nil, err
		}
		places = append(places, p)
	}
	return places, rows.Err()
}

// InsertPlace persists p and sets p.ID and p.CreatedAt.
func (s *Store) InsertPlace(p *Place) error {
	p.CreatedAt = now()
	res, err := s.db.Exec(`INSERT INTO places (label, lat, lng, created_at) VALUES (?,?,?,?)`,
		p.Label, p.Lat, p.Lng, p.CreatedAt)
	if err != nil {
		return err
	}
	p.ID, err = res.LastInsertId()
	return err
}

// DeletePlace removes a place by id. Returns false if id is missing.
func (s *Store) DeletePlace(id int64) (bool, error) {
	res, err := s.db.Exec(`DELETE FROM places WHERE id = ?`, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}
