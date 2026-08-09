package main

import (
	"math"
	"testing"
)

func TestCalculateFare(t *testing.T) {
	// Fixed rate card for reproducible arithmetic tests (not production rates).
	std := Rates{
		Currency:    "ETB",
		BaseFare:    50,
		PerKM:       12,
		PerMinute:   2,
		MinimumFare: 80,
		Rounding:    5,
	}

	tests := []struct {
		name        string
		distanceKM  float64
		durationMin float64
		multiplier  float64
		rates       Rates
		wantFare    float64
		wantMinHit  bool
	}{
		{
			// 50 + 12.4*12 + 28*2 = 254.8 -> 255
			name:       "ordinary trip",
			distanceKM: 12.4, durationMin: 28, multiplier: 1,
			rates: std, wantFare: 255,
		},
		{
			// 50 + 1*12 + 4*2 = 70 < 80 minimum -> 80
			name:       "minimum fare kicks in",
			distanceKM: 1, durationMin: 4, multiplier: 1,
			rates: std, wantFare: 80, wantMinHit: true,
		},
		{
			// minimum applies before the multiplier: 80 * 1.5 = 120
			name:       "multiplier applied after minimum",
			distanceKM: 1, durationMin: 4, multiplier: 1.5,
			rates: std, wantFare: 120, wantMinHit: true,
		},
		{
			// 254.8 * 1.25 = 318.5 -> 320
			name:       "multiplier then rounding",
			distanceKM: 12.4, durationMin: 28, multiplier: 1.25,
			rates: std, wantFare: 320,
		},
		{
			name:       "zero distance still costs the minimum",
			distanceKM: 0, durationMin: 0, multiplier: 1,
			rates: std, wantFare: 80, wantMinHit: true,
		},
		{
			// per-minute disabled: 50 + 12.4*12 = 198.8 -> 200
			name:       "price_per_minute set to zero",
			distanceKM: 12.4, durationMin: 28, multiplier: 1,
			rates:    func() Rates { r := std; r.PerMinute = 0; return r }(),
			wantFare: 200,
		},
		{
			// rounding disabled: exact 254.8
			name:       "no rounding",
			distanceKM: 12.4, durationMin: 28, multiplier: 1,
			rates:    func() Rates { r := std; r.Rounding = 0; return r }(),
			wantFare: 254.8,
		},
		{
			// 254.8 -> nearest 10 is 250
			name:       "rounding to nearest 10",
			distanceKM: 12.4, durationMin: 28, multiplier: 1,
			rates:    func() Rates { r := std; r.Rounding = 10; return r }(),
			wantFare: 250,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CalculateFare(tt.distanceKM, tt.durationMin, tt.multiplier, tt.rates)
			if math.Abs(got.Fare-tt.wantFare) > 1e-9 {
				t.Errorf("fare = %v, want %v (breakdown %+v)", got.Fare, tt.wantFare, got)
			}
			if got.MinimumHit != tt.wantMinHit {
				t.Errorf("minimum_hit = %v, want %v", got.MinimumHit, tt.wantMinHit)
			}
		})
	}
}

func TestBreakdownReproducible(t *testing.T) {
	r := Rates{BaseFare: 50, PerKM: 12, PerMinute: 2, MinimumFare: 80, Rounding: 5}
	b := CalculateFare(7.3, 19, 1, r)

	if got := b.Base + b.DistanceCost + b.DurationCost; math.Abs(got-b.Raw) > 1e-9 {
		t.Errorf("breakdown parts sum to %v, raw says %v", got, b.Raw)
	}
}
