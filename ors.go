package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// HeiGIT OpenRouteService directions + Pelias geocoding (same API key).

func newORSRouter(cfg *Config) (Router, error) {
	if cfg.Routing.APIKey == "" {
		return nil, fmt.Errorf("routing.api_key is empty (set ORS_API_KEY)")
	}
	base := cfg.Routing.BaseURL
	if base == "" {
		base = "https://api.heigit.org/openrouteservice"
	}
	return &orsRouter{baseURL: base, apiKey: cfg.Routing.APIKey, client: httpClient()}, nil
}

type orsRouter struct {
	baseURL string
	apiKey  string
	client  *http.Client
}

type orsResponse struct {
	Routes []orsRoute `json:"routes"`
}

type orsRoute struct {
	Summary  orsSummary `json:"summary"`
	Geometry string     `json:"geometry"`
}

type orsSummary struct {
	Distance float64 `json:"distance"` // metres
	Duration float64 `json:"duration"` // seconds
}

func (o *orsRouter) Route(ctx context.Context, from, to Point) (Route, error) {
	body, _ := json.Marshal(map[string]any{
		"coordinates": [][]float64{{from.Lng, from.Lat}, {to.Lng, to.Lat}},
		"radiuses":    []int{5000, 5000},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		o.baseURL+"/v2/directions/driving-car/json", bytes.NewReader(body))
	if err != nil {
		return Route{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", o.apiKey)

	resp, err := o.client.Do(req)
	if err != nil {
		return Route{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		err := fmt.Errorf("ors: %s: %s", resp.Status, msg)
		if resp.StatusCode >= 400 && resp.StatusCode < 500 {
			if isORSUnroutable(msg) {
				err = fmt.Errorf("%w: %v", ErrUnroutable, err)
			}
			return Route{}, permanentRouteError{err}
		}
		return Route{}, err
	}

	var out orsResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return Route{}, fmt.Errorf("ors: decode: %w", err)
	}
	if len(out.Routes) == 0 {
		return Route{}, fmt.Errorf("ors: no route found")
	}
	r := out.Routes[0]
	return Route{
		DistanceKM:  r.Summary.Distance / 1000,
		DurationMin: r.Summary.Duration / 60,
		Geometry:    r.Geometry,
	}, nil
}

func newPeliasGeocoder(cfg *Config) Geocoder {
	orsKey := strings.TrimSpace(cfg.Routing.APIKey)
	if orsKey == "" {
		return nil
	}
	base := strings.TrimSpace(cfg.Geocoding.PeliasBaseURL)
	if base == "" {
		base = "https://api.heigit.org/pelias/v1"
	}
	return &peliasGeocoder{baseURL: base, apiKey: orsKey, client: httpClient()}
}

type peliasGeocoder struct {
	baseURL string
	apiKey  string
	client  *http.Client
}

type peliasResponse struct {
	Features []peliasFeature `json:"features"`
}

type peliasFeature struct {
	Properties struct {
		Label string `json:"label"`
		Name  string `json:"name"`
	} `json:"properties"`
	Geometry struct {
		Coordinates []float64 `json:"coordinates"` // [lng, lat]
	} `json:"geometry"`
}

func (p *peliasGeocoder) Autocomplete(ctx context.Context, q string, near Point) ([]PlaceHit, error) {
	u, _ := url.Parse(p.baseURL + "/autocomplete")
	qs := u.Query()
	qs.Set("text", q)
	qs.Set("focus.point.lat", fmt.Sprintf("%f", near.Lat))
	qs.Set("focus.point.lon", fmt.Sprintf("%f", near.Lng))
	qs.Set("boundary.country", "ET")
	qs.Set("size", "5")
	u.RawQuery = qs.Encode()

	var out peliasResponse
	if err := p.getJSON(ctx, u.String(), &out); err != nil {
		return nil, err
	}
	return peliasHits(out.Features), nil
}

func (p *peliasGeocoder) Reverse(ctx context.Context, pt Point) (string, error) {
	u, _ := url.Parse(p.baseURL + "/reverse")
	qs := u.Query()
	qs.Set("point.lat", fmt.Sprintf("%f", pt.Lat))
	qs.Set("point.lon", fmt.Sprintf("%f", pt.Lng))
	qs.Set("boundary.country", "ET")
	qs.Set("size", "1")
	u.RawQuery = qs.Encode()

	var out peliasResponse
	if err := p.getJSON(ctx, u.String(), &out); err != nil {
		return "", err
	}
	hits := peliasHits(out.Features)
	if len(hits) == 0 {
		return "", nil
	}
	return hits[0].Label, nil
}

func (p *peliasGeocoder) getJSON(ctx context.Context, rawURL string, dest any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", p.apiKey)
	resp, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("pelias: %s: %s", resp.Status, msg)
	}
	if err := json.NewDecoder(resp.Body).Decode(dest); err != nil {
		return fmt.Errorf("pelias: decode: %w", err)
	}
	return nil
}

func isORSUnroutable(body []byte) bool {
	s := string(body)
	return strings.Contains(s, "Could not find routable point") ||
		strings.Contains(s, `"code":2010`) ||
		strings.Contains(s, `"code": 2010`)
}

func peliasHits(features []peliasFeature) []PlaceHit {
	hits := make([]PlaceHit, 0, len(features))
	for _, f := range features {
		if len(f.Geometry.Coordinates) < 2 {
			continue
		}
		label := strings.TrimSpace(f.Properties.Label)
		if label == "" {
			label = strings.TrimSpace(f.Properties.Name)
		}
		if label == "" {
			continue
		}
		hits = append(hits, PlaceHit{
			Label: label,
			Lat:   f.Geometry.Coordinates[1],
			Lng:   f.Geometry.Coordinates[0],
		})
	}
	return hits
}
