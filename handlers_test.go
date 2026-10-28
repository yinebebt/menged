package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEstimateWithStopAndWaiting(t *testing.T) {
	var gotPts []Point
	router := RouterFunc(func(ctx context.Context, pts []Point) (Route, error) {
		gotPts = pts
		return Route{DistanceKM: 10, DurationMin: 20, Geometry: "x"}, nil
	})
	cfg := &Config{Fare: FareConfig{
		Currency: "ETB", BaseFare: 50, PricePerKM: 10, PricePerMinute: 2,
		PricePerWait: 3, MinimumFare: 80, Rounding: 5,
	}}
	h := NewServer(cfg, nil, router, nil).routes(http.NotFoundHandler())

	body := `{"pickup":{"lat":9.01,"lng":38.76},"stop":{"lat":9.015,"lng":38.765},
		"dest":{"lat":9.02,"lng":38.77},"waiting_min":10}`
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/estimate", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if len(gotPts) != 3 || gotPts[1] != (Point{Lat: 9.015, Lng: 38.765}) {
		t.Fatalf("router points: %#v", gotPts)
	}
	var out estimateResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	// 50 + 10×10 + 20×2 + 10×3 = 220
	if out.Fare != 220 || out.Breakdown.WaitingCost != 30 {
		t.Fatalf("fare=%v waiting_cost=%v", out.Fare, out.Breakdown.WaitingCost)
	}

	rec = httptest.NewRecorder()
	bad := `{"pickup":{"lat":9.01,"lng":38.76},"dest":{"lat":9.02,"lng":38.77},"waiting_min":-1}`
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/estimate", strings.NewReader(bad)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("negative waiting: status %d", rec.Code)
	}
}
