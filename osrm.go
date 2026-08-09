package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

func newOSRMRouter(cfg *Config) (Router, error) {
	if cfg.Routing.BaseURL == "" {
		return nil, fmt.Errorf("routing.base_url is required for osrm")
	}
	return &osrmRouter{baseURL: cfg.Routing.BaseURL, client: httpClient()}, nil
}

type osrmRouter struct {
	baseURL string
	client  *http.Client
}

type osrmResponse struct {
	Code   string      `json:"code"`
	Routes []osrmRoute `json:"routes"`
}

type osrmRoute struct {
	Distance float64 `json:"distance"` // metres
	Duration float64 `json:"duration"` // seconds
	Geometry string  `json:"geometry"`
}

func (o *osrmRouter) Route(ctx context.Context, from, to Point) (Route, error) {
	u := fmt.Sprintf("%s/route/v1/driving/%f,%f;%f,%f?%s",
		o.baseURL, from.Lng, from.Lat, to.Lng, to.Lat,
		url.Values{"overview": {"full"}, "geometries": {"polyline"}}.Encode())

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return Route{}, err
	}
	resp, err := o.client.Do(req)
	if err != nil {
		return Route{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return Route{}, fmt.Errorf("osrm: %s: %s", resp.Status, msg)
	}

	var out osrmResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return Route{}, fmt.Errorf("osrm: decode: %w", err)
	}
	if out.Code != "Ok" || len(out.Routes) == 0 {
		return Route{}, fmt.Errorf("osrm: no route (code %s)", out.Code)
	}
	r := out.Routes[0]
	return Route{
		DistanceKM:  r.Distance / 1000,
		DurationMin: r.Duration / 60,
		Geometry:    r.Geometry,
	}, nil
}
