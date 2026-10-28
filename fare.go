package main

import "math"

// Rates is the rate card loaded from config and snapshotted onto saved trips.
type Rates struct {
	Currency    string  `json:"currency"`
	BaseFare    float64 `json:"base_fare"`
	PerKM       float64 `json:"price_per_km"`
	PerMinute   float64 `json:"price_per_minute"`
	PerWaitMin  float64 `json:"price_per_waiting_minute"`
	MinimumFare float64 `json:"minimum_fare"`
	Rounding    float64 `json:"rounding"`
}

// Breakdown explains how a fare was assembled.
type Breakdown struct {
	Base         float64 `json:"base"`
	DistanceCost float64 `json:"distance_cost"`
	DurationCost float64 `json:"duration_cost"`
	WaitingCost  float64 `json:"waiting_cost"`
	Raw          float64 `json:"raw"`
	MinimumHit   bool    `json:"minimum_hit"`
	Multiplier   float64 `json:"multiplier"`
	Fare         float64 `json:"fare"`
}

// CalculateFare applies the fare model. waitingMin is time spent stopped
// (waiting for the passenger or at a stop), priced separately from driving time.
func CalculateFare(distanceKM, durationMin, waitingMin, multiplier float64, r Rates) Breakdown {
	b := Breakdown{
		Base:         r.BaseFare,
		DistanceCost: distanceKM * r.PerKM,
		DurationCost: durationMin * r.PerMinute,
		WaitingCost:  waitingMin * r.PerWaitMin,
		Multiplier:   multiplier,
	}
	b.Raw = b.Base + b.DistanceCost + b.DurationCost + b.WaitingCost

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
