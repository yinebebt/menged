package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Service-area bounds (roughly Ethiopia). Out-of-range pins fail fast.
const (
	minLat, maxLat = 3.0, 15.5
	minLng, maxLng = 32.5, 48.5
	cacheTTL       = 5 * time.Minute
)

// Server is the HTTP API and static-file front for menged.
type Server struct {
	cfg      *Config
	store    *Store
	router   Router
	geocoder Geocoder

	mu    sync.Mutex
	cache map[string]cachedRoute
}

type cachedRoute struct {
	route Route
	at    time.Time
}

// NewServer wires config, store, router, and optional geocoder into an HTTP server.
func NewServer(cfg *Config, store *Store, router Router, geocoder Geocoder) *Server {
	return &Server{
		cfg: cfg, store: store, router: router, geocoder: geocoder,
		cache: map[string]cachedRoute{},
	}
}

func (s *Server) routes(static http.Handler) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/health", s.health)
	mux.HandleFunc("GET /api/config", s.config)
	mux.HandleFunc("POST /api/estimate", s.estimate)
	mux.HandleFunc("GET /api/geocode", s.geocode)
	mux.HandleFunc("GET /api/reverse", s.reverse)

	mux.HandleFunc("GET /api/trips", s.listTrips)
	mux.HandleFunc("POST /api/trips", s.saveTrip)
	mux.HandleFunc("PATCH /api/trips/{id}", s.editTrip)
	mux.HandleFunc("DELETE /api/trips/{id}", s.deleteTrip)
	mux.HandleFunc("GET /api/trips.csv", s.exportCSV)

	mux.HandleFunc("GET /api/places", s.listPlaces)
	mux.HandleFunc("POST /api/places", s.addPlace)
	mux.HandleFunc("DELETE /api/places/{id}", s.deletePlace)

	mux.Handle("GET /", static)

	return mux
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// Default MapLibre camera for Addis Ababa.
var (
	addisCenter = Point{Lat: 8.9800, Lng: 38.7900}
	addisZoom   = 12.0
	// Keep pan area over road-dense Greater Addis (avoid empty SW fringe).
	addisMaxBounds = [][]float64{{38.65, 8.85}, {38.95, 9.12}} // SW, NE
)

type configResponse struct {
	Rates        Rates       `json:"rates"`
	MapStyle     string      `json:"map_style"`
	Geocode      bool        `json:"geocode"`
	MapCenter    Point       `json:"map_center"`
	MapZoom      float64     `json:"map_zoom"`
	MapMaxBounds [][]float64 `json:"map_max_bounds"`
}

func (s *Server) config(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, configResponse{
		Rates:        s.cfg.Rates(),
		MapStyle:     s.cfg.Server.MapStyle,
		Geocode:      s.geocoder != nil,
		MapCenter:    addisCenter,
		MapZoom:      addisZoom,
		MapMaxBounds: addisMaxBounds,
	})
}

func (s *Server) geocode(w http.ResponseWriter, r *http.Request) {
	if s.geocoder == nil {
		writeError(w, http.StatusServiceUnavailable, "geocoding not configured")
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(q) < 2 {
		writeJSON(w, http.StatusOK, []PlaceHit{})
		return
	}
	near := addisCenter
	if lat, err := strconv.ParseFloat(r.URL.Query().Get("lat"), 64); err == nil {
		if lng, err := strconv.ParseFloat(r.URL.Query().Get("lng"), 64); err == nil {
			near = Point{Lat: lat, Lng: lng}
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
	defer cancel()
	hits, err := s.geocoder.Autocomplete(ctx, q, near)
	if err != nil {
		s.serverError(w, "geocode", err)
		return
	}
	writeJSON(w, http.StatusOK, hits)
}

func (s *Server) reverse(w http.ResponseWriter, r *http.Request) {
	if s.geocoder == nil {
		writeError(w, http.StatusServiceUnavailable, "geocoding not configured")
		return
	}
	lat, err1 := strconv.ParseFloat(r.URL.Query().Get("lat"), 64)
	lng, err2 := strconv.ParseFloat(r.URL.Query().Get("lng"), 64)
	if err1 != nil || err2 != nil {
		writeError(w, http.StatusBadRequest, "lat and lng required")
		return
	}
	p := Point{Lat: lat, Lng: lng}
	if p.Lat < minLat || p.Lat > maxLat || p.Lng < minLng || p.Lng > maxLng {
		writeError(w, http.StatusBadRequest, "point outside the service area")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
	defer cancel()
	label, err := s.geocoder.Reverse(ctx, p)
	if err != nil {
		s.serverError(w, "reverse", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"label": label})
}

type estimateRequest struct {
	Pickup     Point   `json:"pickup"`
	Dest       Point   `json:"dest"`
	Multiplier float64 `json:"multiplier"`
}

type estimateResponse struct {
	DistanceKM  float64   `json:"distance_km"`
	DurationMin float64   `json:"duration_min"`
	Geometry    string    `json:"geometry"`
	Fare        float64   `json:"fare"`
	Breakdown   Breakdown `json:"breakdown"`
	Rates       Rates     `json:"rates"`
}

func (s *Server) estimate(w http.ResponseWriter, r *http.Request) {
	var req estimateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad json: "+err.Error())
		return
	}
	if req.Multiplier <= 0 {
		req.Multiplier = 1
	}
	for _, p := range []Point{req.Pickup, req.Dest} {
		if p.Lat < minLat || p.Lat > maxLat || p.Lng < minLng || p.Lng > maxLng {
			writeError(w, http.StatusBadRequest, "point outside the service area")
			return
		}
	}

	route, err := s.cachedOrLiveRoute(r, req.Pickup, req.Dest)
	if err != nil {
		slog.Error("routing failed", "err", err,
			"pickup", fmt.Sprintf("%.5f,%.5f", req.Pickup.Lat, req.Pickup.Lng),
			"dest", fmt.Sprintf("%.5f,%.5f", req.Dest.Lat, req.Dest.Lng))
		msg := "routing failed — try again"
		if strings.Contains(err.Error(), "Could not find routable point") {
			msg = "no road near that pin — move it closer to a street"
			writeError(w, http.StatusUnprocessableEntity, msg)
			return
		}
		writeError(w, http.StatusBadGateway, msg)
		return
	}

	rates := s.cfg.Rates()
	breakdown := CalculateFare(route.DistanceKM, route.DurationMin, req.Multiplier, rates)

	writeJSON(w, http.StatusOK, estimateResponse{
		DistanceKM:  route.DistanceKM,
		DurationMin: route.DurationMin,
		Geometry:    route.Geometry,
		Fare:        breakdown.Fare,
		Breakdown:   breakdown,
		Rates:       rates,
	})
}

// cachedOrLiveRoute returns a cached route when the same ~10 m–rounded
// coordinate pair was requested within cacheTTL.
func (s *Server) cachedOrLiveRoute(r *http.Request, from, to Point) (Route, error) {
	key := fmt.Sprintf("%.4f,%.4f|%.4f,%.4f", from.Lat, from.Lng, to.Lat, to.Lng)

	s.mu.Lock()
	if c, ok := s.cache[key]; ok && time.Since(c.at) < cacheTTL {
		s.mu.Unlock()
		return c.route, nil
	}
	s.mu.Unlock()

	var route Route
	var err error
	ctx := r.Context()
	for attempt := 0; attempt < 2; attempt++ {
		route, err = s.router.Route(ctx, from, to)
		if err == nil || ctx.Err() != nil || !isRetryableRoute(err) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		return Route{}, err
	}

	s.mu.Lock()
	if len(s.cache) > 500 {
		clear(s.cache)
	}
	s.cache[key] = cachedRoute{route: route, at: time.Now()}
	s.mu.Unlock()
	return route, nil
}

func (s *Server) listTrips(w http.ResponseWriter, r *http.Request) {
	trips, err := s.store.ListTrips()
	if err != nil {
		s.serverError(w, "list trips", err)
		return
	}
	writeJSON(w, http.StatusOK, trips)
}

func (s *Server) saveTrip(w http.ResponseWriter, r *http.Request) {
	var t Trip
	if err := json.NewDecoder(r.Body).Decode(&t); err != nil {
		writeError(w, http.StatusBadRequest, "bad json: "+err.Error())
		return
	}
	if t.DistanceKM <= 0 || t.SuggestedFare <= 0 {
		writeError(w, http.StatusBadRequest, "trip has no estimate")
		return
	}
	if t.Multiplier <= 0 {
		t.Multiplier = 1
	}
	// Snapshot the rate card used for this trip.
	rates := s.cfg.Rates()
	t.RateBase, t.RatePerKM, t.RatePerMin = rates.BaseFare, rates.PerKM, rates.PerMinute

	if err := s.store.InsertTrip(&t); err != nil {
		s.serverError(w, "save trip", err)
		return
	}
	writeJSON(w, http.StatusCreated, t)
}

// tripPatch is the body of PATCH /api/trips/{id}.
type tripPatch struct {
	ActualCharged *float64 `json:"actual_charged"`
	Note          *string  `json:"note"`
}

func (s *Server) editTrip(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad id")
		return
	}
	var patch tripPatch
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		writeError(w, http.StatusBadRequest, "bad json: "+err.Error())
		return
	}
	ok, err := s.store.UpdateTrip(id, patch.ActualCharged, patch.Note)
	if err != nil {
		s.serverError(w, "edit trip", err)
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "no such trip")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) deleteTrip(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad id")
		return
	}
	ok, err := s.store.DeleteTrip(id)
	if err != nil {
		s.serverError(w, "delete trip", err)
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "no such trip")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) exportCSV(w http.ResponseWriter, r *http.Request) {
	trips, err := s.store.ListTrips()
	if err != nil {
		s.serverError(w, "export csv", err)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="menged-trips.csv"`)

	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"created_at", "pickup", "pickup_lat", "pickup_lng",
		"destination", "dest_lat", "dest_lng", "distance_km", "duration_min",
		"suggested_fare", "actual_charged", "multiplier",
		"rate_base", "rate_per_km", "rate_per_min", "note"})
	f := func(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }
	for _, t := range trips {
		charged := ""
		if t.ActualCharged != nil {
			charged = f(*t.ActualCharged)
		}
		_ = cw.Write([]string{t.CreatedAt, t.PickupLabel, f(t.PickupLat), f(t.PickupLng),
			t.DestLabel, f(t.DestLat), f(t.DestLng), f(t.DistanceKM), f(t.DurationMin),
			f(t.SuggestedFare), charged, f(t.Multiplier),
			f(t.RateBase), f(t.RatePerKM), f(t.RatePerMin), t.Note})
	}
	cw.Flush()
}

func (s *Server) listPlaces(w http.ResponseWriter, r *http.Request) {
	places, err := s.store.ListPlaces()
	if err != nil {
		s.serverError(w, "list places", err)
		return
	}
	writeJSON(w, http.StatusOK, places)
}

func (s *Server) addPlace(w http.ResponseWriter, r *http.Request) {
	var p Place
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeError(w, http.StatusBadRequest, "bad json: "+err.Error())
		return
	}
	p.Label = strings.TrimSpace(p.Label)
	if p.Label == "" {
		writeError(w, http.StatusBadRequest, "label is required")
		return
	}
	if p.Lat < minLat || p.Lat > maxLat || p.Lng < minLng || p.Lng > maxLng {
		writeError(w, http.StatusBadRequest, "point outside the service area")
		return
	}
	if err := s.store.InsertPlace(&p); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			writeError(w, http.StatusConflict, "a place with that name already exists")
			return
		}
		s.serverError(w, "add place", err)
		return
	}
	writeJSON(w, http.StatusCreated, p)
}

func (s *Server) deletePlace(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad id")
		return
	}
	ok, err := s.store.DeletePlace(id)
	if err != nil {
		s.serverError(w, "delete place", err)
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "no such place")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// serverError logs op and responds with HTTP 500.
func (s *Server) serverError(w http.ResponseWriter, op string, err error) {
	slog.Error(op, "err", err)
	writeError(w, http.StatusInternalServerError, err.Error())
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
