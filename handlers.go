package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const maxJSONBody = 1 << 20 // 1 MiB

// Server is the HTTP API and static-file front for menged.
type Server struct {
	cfg      *Config
	store    *Store
	router   Router
	geocoder Geocoder
}

// NewServer wires config, store, router, and geocoder into an HTTP server.
func NewServer(cfg *Config, store *Store, router Router, geocoder Geocoder) *Server {
	return &Server{cfg: cfg, store: store, router: router, geocoder: geocoder}
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

	mux.HandleFunc("GET /api/places", s.listPlaces)
	mux.HandleFunc("POST /api/places", s.addPlace)
	mux.HandleFunc("DELETE /api/places/{id}", s.deletePlace)

	mux.Handle("GET /", noCache(static))

	return mux
}

func noCache(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache, must-revalidate")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// Default MapLibre camera for Addis Ababa.
var (
	addisCenter    = Point{Lat: 8.9800, Lng: 38.7900}
	addisZoom      = 12.0
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
	if !InServiceArea(p) {
		writeError(w, http.StatusBadRequest, ErrOutsideArea.Error())
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
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad json: "+err.Error())
		return
	}
	if req.Multiplier <= 0 {
		req.Multiplier = 1
	}
	for _, p := range []Point{req.Pickup, req.Dest} {
		if !InServiceArea(p) {
			writeError(w, http.StatusBadRequest, ErrOutsideArea.Error())
			return
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	route, err := s.router.Route(ctx, req.Pickup, req.Dest)
	if err != nil {
		slog.Error("routing failed", "err", err,
			"pickup", fmt.Sprintf("%.5f,%.5f", req.Pickup.Lat, req.Pickup.Lng),
			"dest", fmt.Sprintf("%.5f,%.5f", req.Dest.Lat, req.Dest.Lng))
		if errors.Is(err, ErrUnroutable) {
			writeError(w, http.StatusUnprocessableEntity, ErrUnroutable.Error())
			return
		}
		writeError(w, http.StatusBadGateway, "routing failed — try again")
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
	if err := decodeJSON(w, r, &t); err != nil {
		writeError(w, http.StatusBadRequest, "bad json: "+err.Error())
		return
	}
	if t.DistanceKM <= 0 || t.DurationMin < 0 || t.SuggestedFare <= 0 {
		writeError(w, http.StatusBadRequest, "trip has no estimate")
		return
	}
	for _, p := range []Point{
		{Lat: t.PickupLat, Lng: t.PickupLng},
		{Lat: t.DestLat, Lng: t.DestLng},
	} {
		if !InServiceArea(p) {
			writeError(w, http.StatusBadRequest, ErrOutsideArea.Error())
			return
		}
	}
	if t.Multiplier <= 0 {
		t.Multiplier = 1
	}
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
	if err := decodeJSON(w, r, &patch); err != nil {
		writeError(w, http.StatusBadRequest, "bad json: "+err.Error())
		return
	}
	if patch.ActualCharged == nil && patch.Note == nil {
		writeError(w, http.StatusBadRequest, "nothing to update")
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
	if err := decodeJSON(w, r, &p); err != nil {
		writeError(w, http.StatusBadRequest, "bad json: "+err.Error())
		return
	}
	p.Label = strings.TrimSpace(p.Label)
	if p.Label == "" {
		writeError(w, http.StatusBadRequest, "label is required")
		return
	}
	if !InServiceArea(Point{Lat: p.Lat, Lng: p.Lng}) {
		writeError(w, http.StatusBadRequest, ErrOutsideArea.Error())
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

func (s *Server) serverError(w http.ResponseWriter, op string, err error) {
	slog.Error(op, "err", err)
	writeError(w, http.StatusInternalServerError, "internal error")
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBody)
	dec := json.NewDecoder(r.Body)
	err := dec.Decode(dst)
	if err == nil {
		return nil
	}
	var maxErr *http.MaxBytesError
	if errors.As(err, &maxErr) || errors.Is(err, io.ErrUnexpectedEOF) {
		return fmt.Errorf("body too large")
	}
	return err
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
