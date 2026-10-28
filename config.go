package main

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Config is the YAML configuration loaded at startup.
type Config struct {
	Fare      FareConfig      `yaml:"fare"`
	Routing   RoutingConfig   `yaml:"routing"`
	Geocoding GeocodingConfig `yaml:"geocoding"`
	Server    ServerConfig    `yaml:"server"`
}

// FareConfig is the rate card.
type FareConfig struct {
	Currency       string  `yaml:"currency"`
	BaseFare       float64 `yaml:"base_fare"`
	PricePerKM     float64 `yaml:"price_per_km"`
	PricePerMinute float64 `yaml:"price_per_minute"`
	PricePerWait   float64 `yaml:"price_per_waiting_minute"`
	MinimumFare    float64 `yaml:"minimum_fare"`
	Rounding       float64 `yaml:"rounding"`
}

// RoutingConfig selects the route provider.
type RoutingConfig struct {
	Provider string `yaml:"provider"` // "ors" or "osrm"
	APIKey   string `yaml:"api_key"`
	BaseURL  string `yaml:"base_url"`
}

// GeocodingConfig configures Pelias place search / reverse labels.
// Provider: pelias (default) or off. Uses routing.api_key.
type GeocodingConfig struct {
	Provider      string `yaml:"provider"`
	PeliasBaseURL string `yaml:"pelias_base_url"` // empty → https://api.heigit.org/pelias/v1
}

// ServerConfig is listen address, database path, and map style.
type ServerConfig struct {
	Listen   string `yaml:"listen"`
	DBPath   string `yaml:"db_path"`
	MapStyle string `yaml:"map_style"`
}

// LoadConfig reads path, expands ${ENV} references, and applies defaults.
func LoadConfig(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	expanded := os.Expand(string(raw), os.Getenv)

	cfg := &Config{}
	cfg.Fare.Currency = "ETB"
	cfg.Fare.Rounding = 5
	cfg.Routing.Provider = "ors"
	cfg.Geocoding.Provider = "pelias"
	cfg.Server.Listen = "127.0.0.1:8080"
	cfg.Server.DBPath = "./menged.db"
	cfg.Server.MapStyle = "https://tiles.openfreemap.org/styles/liberty"

	if err := yaml.Unmarshal([]byte(expanded), cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return cfg, nil
}

// Rates returns the fare rate card for pricing and API config responses.
func (c *Config) Rates() Rates {
	return Rates{
		Currency:    c.Fare.Currency,
		BaseFare:    c.Fare.BaseFare,
		PerKM:       c.Fare.PricePerKM,
		PerMinute:   c.Fare.PricePerMinute,
		PerWaitMin:  c.Fare.PricePerWait,
		MinimumFare: c.Fare.MinimumFare,
		Rounding:    c.Fare.Rounding,
	}
}
