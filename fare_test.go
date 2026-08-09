package main

import (
	"math"
	"testing"
)

func TestCalculateFare(t *testing.T) {
	std := Rates{
		Currency: "ETB", BaseFare: 50, PerKM: 12, PerMinute: 2,
		MinimumFare: 80, Rounding: 5,
	}
	tests := []struct {
		name                    string
		km, min, mult, wantFare float64
		rates                   Rates
		wantMin                 bool
	}{
		{"ordinary", 12.4, 28, 1, 255, std, false},
		{"minimum", 1, 4, 1, 80, std, true},
		{"minimum then multiplier", 1, 4, 1.5, 120, std, true},
		{"multiplier then round", 12.4, 28, 1.25, 320, std, false},
		{"no per-minute", 12.4, 28, 1, 200, Rates{BaseFare: 50, PerKM: 12, MinimumFare: 80, Rounding: 5}, false},
		{"no rounding", 12.4, 28, 1, 254.8, Rates{BaseFare: 50, PerKM: 12, PerMinute: 2, MinimumFare: 80}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CalculateFare(tt.km, tt.min, tt.mult, tt.rates)
			if math.Abs(got.Fare-tt.wantFare) > 1e-9 {
				t.Fatalf("fare=%v want=%v", got.Fare, tt.wantFare)
			}
			if got.MinimumHit != tt.wantMin {
				t.Fatalf("minimum_hit=%v want=%v", got.MinimumHit, tt.wantMin)
			}
		})
	}
}
