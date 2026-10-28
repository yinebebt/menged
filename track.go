package main

import (
	"math"
	"strings"
)

// Trip recording: turn the GPS fixes the browser collected during a trip into
// distance, moving time and waiting time.

// TrackPoint is one GPS fix from the browser.
type TrackPoint struct {
	Lat float64 `json:"lat"`
	Lng float64 `json:"lng"`
	T   int64   `json:"t"`   // unix milliseconds
	Acc float64 `json:"acc"` // reported accuracy in metres; 0 if unknown
}

func (p TrackPoint) point() Point { return Point{Lat: p.Lat, Lng: p.Lng} }

const (
	maxFixAccuracyM = 50  // fixes less accurate than this are dropped
	minMoveM        = 20  // a move shorter than this, or than the fix's accuracy, is jitter
	waitSpeedKmh    = 5.0 // slower than this counts as waiting
	maxTrackPoints  = 20000
	routeSampleM    = 300 // spacing of the points sent to the router
	maxRoutePoints  = 25
)

// Measurement is what a recorded track adds up to.
type Measurement struct {
	GPSDistanceKM float64
	MovingMin     float64
	WaitingMin    float64
	Path          []Point // fixes that counted as movement, starting with the first fix
	Rough         bool    // no fix met maxFixAccuracyM, so rough fixes were used
}

// measureTrack walks the usable fixes from an anchor, ignoring moves smaller than
// GPS jitter. Each real move is driving or waiting by its average speed, so a stop
// (or crawling traffic) between two moves is waiting time. Time after the last move
// is not counted: that is the driver arriving and pressing End.
//
// Fixes less accurate than maxFixAccuracyM are dropped, unless none are better:
// then the rough ones are used (Wi-Fi location on a laptop, a phone indoors). They
// can't add phantom distance, because a move must exceed the fix's own accuracy.
func measureTrack(track []TrackPoint) Measurement {
	var inArea, accurate []TrackPoint
	for _, p := range track {
		if !InServiceArea(p.point()) {
			continue
		}
		if len(inArea) > 0 && p.T <= inArea[len(inArea)-1].T {
			continue
		}
		inArea = append(inArea, p)
		if p.Acc <= maxFixAccuracyM {
			accurate = append(accurate, p)
		}
	}
	var m Measurement
	fixes := accurate
	if len(fixes) == 0 {
		fixes, m.Rough = inArea, true
	}
	if len(fixes) == 0 {
		return m
	}

	m.Path = []Point{fixes[0].point()}
	anchor := fixes[0]
	var distM, movingSec, waitingSec float64
	for _, p := range fixes[1:] {
		d := haversineM(anchor.point(), p.point())
		if d < math.Max(minMoveM, p.Acc) {
			continue
		}
		dt := float64(p.T-anchor.T) / 1000
		if d/dt*3.6 < waitSpeedKmh {
			waitingSec += dt
		} else {
			movingSec += dt
		}
		distM += d
		anchor = p
		m.Path = append(m.Path, p.point())
	}
	m.GPSDistanceKM = distM / 1000
	m.MovingMin = movingSec / 60
	m.WaitingMin = waitingSec / 60
	return m
}

// routeSamples picks points along path about routeSampleM apart, always keeping
// the first and last, so the router can return the road distance of the trip.
func routeSamples(path []Point) []Point {
	if len(path) < 2 {
		return path
	}
	out := []Point{path[0]}
	var since float64
	for i := 1; i < len(path)-1; i++ {
		since += haversineM(path[i-1], path[i])
		if since >= routeSampleM {
			out = append(out, path[i])
			since = 0
		}
	}
	out = append(out, path[len(path)-1])
	if len(out) <= maxRoutePoints {
		return out
	}
	thinned := make([]Point, 0, maxRoutePoints)
	step := float64(len(out)-1) / float64(maxRoutePoints-1)
	for i := 0; i < maxRoutePoints; i++ {
		thinned = append(thinned, out[int(math.Round(float64(i)*step))])
	}
	return thinned
}

// haversineM is the great-circle distance between a and b in metres.
func haversineM(a, b Point) float64 {
	const earthRadiusM = 6371000
	rad := math.Pi / 180
	dLat := (b.Lat - a.Lat) * rad
	dLng := (b.Lng - a.Lng) * rad
	h := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(a.Lat*rad)*math.Cos(b.Lat*rad)*math.Sin(dLng/2)*math.Sin(dLng/2)
	return 2 * earthRadiusM * math.Asin(math.Sqrt(h))
}

// encodePolyline encodes path as a precision-5 Google polyline, the format the
// routers return and the web UI decodes.
func encodePolyline(path []Point) string {
	var sb strings.Builder
	var prevLat, prevLng int64
	for _, p := range path {
		lat := int64(math.Round(p.Lat * 1e5))
		lng := int64(math.Round(p.Lng * 1e5))
		encodeSigned(&sb, lat-prevLat)
		encodeSigned(&sb, lng-prevLng)
		prevLat, prevLng = lat, lng
	}
	return sb.String()
}

func encodeSigned(sb *strings.Builder, v int64) {
	u := v << 1
	if v < 0 {
		u = ^u
	}
	for u >= 0x20 {
		sb.WriteByte(byte((0x20 | (u & 0x1f)) + 63))
		u >>= 5
	}
	sb.WriteByte(byte(u + 63))
}
