package main

import "math"

// Rates is the rate card loaded from config and snapshotted onto saved trips.
type Rates struct {
	Currency    string  `json:"currency"`
	BaseFare    float64 `json:"base_fare"`
	PerKM       float64 `json:"price_per_km"`
	PerMinute   float64 `json:"price_per_minute"`
	MinimumFare float64 `json:"minimum_fare"`
	Rounding    float64 `json:"rounding"`
}

// Breakdown explains how a fare was assembled.
type Breakdown struct {
	Base         float64 `json:"base"`
	DistanceCost float64 `json:"distance_cost"`
	DurationCost float64 `json:"duration_cost"`
	Raw          float64 `json:"raw"`
	MinimumHit   bool    `json:"minimum_hit"`
	Multiplier   float64 `json:"multiplier"`
	Fare         float64 `json:"fare"`
}

// CalculateFare applies the fare model.
func CalculateFare(distanceKM, durationMin, multiplier float64, r Rates) Breakdown {
	b := Breakdown{
		Base:         r.BaseFare,
		DistanceCost: distanceKM * r.PerKM,
		DurationCost: durationMin * r.PerMinute,
		Multiplier:   multiplier,
	}
	b.Raw = b.Base + b.DistanceCost + b.DurationCost

	fare := b.Raw
	if fare < r.MinimumFare {
		fare = r.MinimumFare
		b.MinimumHit = true
	}
	fare *= multiplier
	if r.Rounding > 0 {
		fare = math.Round(fare/r.Rounding) * r.Rounding
	}
	b.Fare = fare
	return b
}
