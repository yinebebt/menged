package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	mailer   Mailer
}

// NewServer wires config, store, router, geocoder, and mailer into an HTTP server.
func NewServer(cfg *Config, store *Store, router Router, geocoder Geocoder, mailer Mailer) *Server {
	return &Server{cfg: cfg, store: store, router: router, geocoder: geocoder, mailer: mailer}
}

func (s *Server) routes(static http.Handler) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/health", s.health)
	mux.HandleFunc("GET /api/config", s.config)
	mux.HandleFunc("POST /api/estimate", s.estimate)
	mux.HandleFunc("POST /api/measure", s.measure)
	mux.HandleFunc("GET /api/geocode", s.geocode)
	mux.HandleFunc("GET /api/reverse", s.reverse)

	mux.HandleFunc("POST /api/signup", s.signup)
	mux.HandleFunc("POST /api/login", s.login)
	mux.HandleFunc("POST /api/password/forgot", s.forgotPassword)
	mux.HandleFunc("POST /api/password/reset", s.resetPassword)
	mux.HandleFunc("POST /api/logout", s.logout)
	mux.HandleFunc("GET /api/me", s.requireUser(s.me))

	mux.HandleFunc("GET /api/trips", s.requireUser(s.listTrips))
	mux.HandleFunc("POST /api/trips", s.requireUser(s.saveTrip))
	mux.HandleFunc("PATCH /api/trips/{id}", s.requireUser(s.editTrip))
	mux.HandleFunc("DELETE /api/trips/{id}", s.requireUser(s.deleteTrip))

	mux.HandleFunc("GET /api/places", s.requireUser(s.listPlaces))
	mux.HandleFunc("POST /api/places", s.requireUser(s.addPlace))
	mux.HandleFunc("DELETE /api/places/{id}", s.requireUser(s.deletePlace))

	mux.Handle("GET /", noCache(static))

	// Refuses state-changing requests from other sites, including sibling
	// subdomains that SameSite cookies would treat as the same site.
	return http.NewCrossOriginProtection().Handler(mux)
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
	Signup       bool        `json:"signup"`
}

func (s *Server) config(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, configResponse{
		Rates:        s.cfg.Rates(),
		MapStyle:     s.cfg.Server.MapStyle,
		Geocode:      s.geocoder != nil,
		MapCenter:    addisCenter,
		MapZoom:      addisZoom,
		MapMaxBounds: addisMaxBounds,
		Signup:       s.cfg.Server.Signup,
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
	Stop       *Point  `json:"stop,omitempty"`
	Dest       Point   `json:"dest"`
	WaitingMin float64 `json:"waiting_min"`
	Multiplier float64 `json:"multiplier"`
}

// maxWaitingMin caps typed-in waiting time; anything longer is a typo.
const maxWaitingMin = 600

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
	if req.WaitingMin < 0 || req.WaitingMin > maxWaitingMin {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("waiting_min must be 0–%d", maxWaitingMin))
		return
	}
	pts := []Point{req.Pickup}
	if req.Stop != nil {
		pts = append(pts, *req.Stop)
	}
	pts = append(pts, req.Dest)
	for _, p := range pts {
		if !InServiceArea(p) {
			writeError(w, http.StatusBadRequest, ErrOutsideArea.Error())
			return
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	route, err := s.router.Route(ctx, pts)
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
	breakdown := CalculateFare(route.DistanceKM, route.DurationMin, req.WaitingMin, req.Multiplier, rates)

	writeJSON(w, http.StatusOK, estimateResponse{
		DistanceKM:  route.DistanceKM,
		DurationMin: route.DurationMin,
		Geometry:    route.Geometry,
		Fare:        breakdown.Fare,
		Breakdown:   breakdown,
		Rates:       rates,
	})
}

type measureRequest struct {
	Track      []TrackPoint `json:"track"`
	Multiplier float64      `json:"multiplier"`
	// Route asks the router for the road distance through the track. The live meter
	// leaves it off so it does not spend routing quota every few seconds.
	Route bool `json:"route"`
}

type measureResponse struct {
	DistanceKM  float64   `json:"distance_km"`
	DurationMin float64   `json:"duration_min"` // moving time
	WaitingMin  float64   `json:"waiting_min"`
	Rough       bool      `json:"rough"`  // only low-accuracy fixes were available
	Method      string    `json:"method"` // "road" (routed through the track) or "gps"
	Geometry    string    `json:"geometry"`
	Track       string    `json:"track"` // the fixes that counted, as a polyline
	Start       Point     `json:"start"`
	End         Point     `json:"end"`
	Fare        float64   `json:"fare"`
	Breakdown   Breakdown `json:"breakdown"`
	Rates       Rates     `json:"rates"`
}

// measure prices a recorded trip from its GPS track.
func (s *Server) measure(w http.ResponseWriter, r *http.Request) {
	var req measureRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad json: "+err.Error())
		return
	}
	if len(req.Track) > maxTrackPoints {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("track has more than %d points", maxTrackPoints))
		return
	}
	if req.Multiplier <= 0 {
		req.Multiplier = 1
	}
	m := measureTrack(req.Track)
	if len(m.Path) == 0 {
		msg := "no GPS fixes recorded yet"
		if len(req.Track) > 0 {
			msg = fmt.Sprintf("all %d GPS fixes are outside the service area", len(req.Track))
		}
		writeError(w, http.StatusUnprocessableEntity, msg)
		return
	}

	track := encodePolyline(m.Path)
	resp := measureResponse{
		DistanceKM:  m.GPSDistanceKM,
		DurationMin: m.MovingMin,
		WaitingMin:  m.WaitingMin,
		Rough:       m.Rough,
		Method:      "gps",
		Geometry:    track,
		Track:       track,
		Start:       m.Path[0],
		End:         m.Path[len(m.Path)-1],
	}
	if samples := routeSamples(m.Path); req.Route && len(samples) >= 2 {
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		route, err := s.router.Route(ctx, samples)
		if err == nil {
			resp.DistanceKM, resp.Geometry, resp.Method = route.DistanceKM, route.Geometry, "road"
		} else {
			slog.Warn("routing a recorded track failed; using GPS distance", "err", err, "points", len(samples))
		}
	}

	resp.Rates = s.cfg.Rates()
	resp.Breakdown = CalculateFare(resp.DistanceKM, resp.DurationMin, resp.WaitingMin, req.Multiplier, resp.Rates)
	resp.Fare = resp.Breakdown.Fare
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) listTrips(w http.ResponseWriter, r *http.Request, u *User) {
	trips, err := s.store.ListTrips(u.ID)
	if err != nil {
		s.serverError(w, "list trips", err)
		return
	}
	writeJSON(w, http.StatusOK, trips)
}

func (s *Server) saveTrip(w http.ResponseWriter, r *http.Request, u *User) {
	var t Trip
	if err := decodeJSON(w, r, &t); err != nil {
		writeError(w, http.StatusBadRequest, "bad json: "+err.Error())
		return
	}
	// A recorded trip can measure 0 km (it never moved); a quote cannot.
	recorded := t.StartedAt != ""
	if (!recorded && t.DistanceKM <= 0) || t.DistanceKM < 0 || t.DurationMin < 0 {
		writeError(w, http.StatusBadRequest, "trip has no estimate")
		return
	}
	if !recorded {
		t.EndedAt, t.Track, t.QuotedFare = "", "", nil
	}
	if (t.StopLat == nil) != (t.StopLng == nil) {
		writeError(w, http.StatusBadRequest, "stop needs both stop_lat and stop_lng")
		return
	}
	if t.WaitingMin < 0 || t.WaitingMin > maxWaitingMin {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("waiting_min must be 0–%d", maxWaitingMin))
		return
	}
	pts := []Point{{Lat: t.PickupLat, Lng: t.PickupLng}, {Lat: t.DestLat, Lng: t.DestLng}}
	if t.StopLat != nil {
		pts = append(pts, Point{Lat: *t.StopLat, Lng: *t.StopLng})
	} else {
		t.StopLabel = ""
	}
	for _, p := range pts {
		if !InServiceArea(p) {
			writeError(w, http.StatusBadRequest, ErrOutsideArea.Error())
			return
		}
	}
	if t.Multiplier <= 0 {
		t.Multiplier = 1
	}
	rates := s.cfg.Rates()
	t.SuggestedFare = CalculateFare(t.DistanceKM, t.DurationMin, t.WaitingMin, t.Multiplier, rates).Fare
	t.RateBase, t.RatePerKM, t.RatePerMin = rates.BaseFare, rates.PerKM, rates.PerMinute
	t.RatePerWaitMin = rates.PerWaitMin
	t.UserID = u.ID

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

func (s *Server) editTrip(w http.ResponseWriter, r *http.Request, u *User) {
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
	ok, err := s.store.UpdateTrip(u.ID, id, patch.ActualCharged, patch.Note)
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

func (s *Server) deleteTrip(w http.ResponseWriter, r *http.Request, u *User) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad id")
		return
	}
	ok, err := s.store.DeleteTrip(u.ID, id)
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

func (s *Server) listPlaces(w http.ResponseWriter, r *http.Request, u *User) {
	places, err := s.store.ListPlaces(u.ID)
	if err != nil {
		s.serverError(w, "list places", err)
		return
	}
	writeJSON(w, http.StatusOK, places)
}

func (s *Server) addPlace(w http.ResponseWriter, r *http.Request, u *User) {
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
	p.UserID = u.ID
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

func (s *Server) deletePlace(w http.ResponseWriter, r *http.Request, u *User) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad id")
		return
	}
	ok, err := s.store.DeletePlace(u.ID, id)
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
	if errors.As(err, &maxErr) {
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
