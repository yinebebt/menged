package main

import (
	"context"
	"embed"
	"errors"
	"flag"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

//go:embed web
var webFS embed.FS

//go:embed schema.sql
var schemaSQL string

func main() {
	configPath := flag.String("config", "config.yaml", "path to config file")
	flag.Parse()

	cfg, err := LoadConfig(*configPath)
	if err != nil {
		fatal("config", err)
	}

	store, err := OpenStore(cfg.Server.DBPath, schemaSQL)
	if err != nil {
		fatal("store", err)
	}
	defer store.Close()

	router, err := NewRouter(cfg)
	if err != nil {
		fatal("routing", err)
	}
	geocoder, err := NewGeocoder(cfg)
	if err != nil {
		fatal("geocoding", err)
	}

	web, err := fs.Sub(webFS, "web")
	if err != nil {
		fatal("embed", err)
	}

	srv := &http.Server{
		Addr:              cfg.Server.Listen,
		Handler:           NewServer(cfg, store, router, geocoder).routes(http.FileServerFS(web)),
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		slog.Info("menged listening",
			"addr", "http://"+cfg.Server.Listen,
			"db", cfg.Server.DBPath,
			"routing", cfg.Routing.Provider,
			"geocode", geocoder != nil)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			fatal("serve", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		slog.Error("shutdown", "err", err)
	}
}

func fatal(msg string, err error) {
	slog.Error(msg, "err", err)
	os.Exit(1)
}
