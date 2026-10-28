package main

import (
	"bufio"
	"context"
	"embed"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"
)

//go:embed web
var webFS embed.FS

//go:embed schema.sql
var schemaSQL string

func main() {
	configPath := flag.String("config", "config.yaml", "path to config file")
	allowPublicListen := flag.Bool("allow-public-listen", false, "allow binding a non-loopback address (unsafe without a TLS reverse proxy)")
	resetEmailFlag := flag.String("reset-password", "", "set a new password for the account with this email, then exit (password read from stdin)")
	flag.Parse()

	cfg, err := LoadConfig(*configPath)
	if err != nil {
		fatal("config", err)
	}

	if *resetEmailFlag != "" {
		store, err := OpenStore(cfg.Server.DBPath, schemaSQL)
		if err != nil {
			fatal("store", err)
		}
		err = resetPasswordCLI(store, *resetEmailFlag)
		store.Close()
		if err != nil {
			fatal("reset password", err)
		}
		return
	}

	if !loopbackListen(cfg.Server.Listen) {
		if !*allowPublicListen {
			fatal("listen", errors.New("refusing non-loopback listen "+cfg.Server.Listen+"; bind 127.0.0.1 or pass -allow-public-listen"))
		}
		slog.Warn("listening on a non-loopback address; sign-in cookies need TLS in front",
			"addr", cfg.Server.Listen)
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
	mailer, err := NewMailer(cfg.Mail)
	if err != nil {
		fatal("mail", err)
	}

	web, err := fs.Sub(webFS, "web")
	if err != nil {
		fatal("embed", err)
	}

	srv := &http.Server{
		Addr:              cfg.Server.Listen,
		Handler:           NewServer(cfg, store, router, geocoder, mailer).routes(http.FileServerFS(web)),
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

// resetPasswordCLI is the fallback when reset emails can't be sent: it reads a new
// password (twice, hidden, on a terminal; one line when piped) for an existing
// account and signs that user out everywhere.
func resetPasswordCLI(store *Store, email string) error {
	u, err := store.UserByEmail(email)
	if err != nil {
		return err
	}
	if u == nil {
		return fmt.Errorf("no account with email %q", email)
	}
	var password string
	if fd := int(os.Stdin.Fd()); term.IsTerminal(fd) {
		fmt.Fprintf(os.Stderr, "New password for %s: ", u.Email)
		first, err := term.ReadPassword(fd)
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return err
		}
		fmt.Fprint(os.Stderr, "Again: ")
		again, err := term.ReadPassword(fd)
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return err
		}
		if string(first) != string(again) {
			return errors.New("passwords don't match")
		}
		password = string(first)
	} else {
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && line == "" {
			return fmt.Errorf("read password from stdin: %w", err)
		}
		password = strings.TrimRight(line, "\r\n")
	}
	if err := store.SetPassword(u.ID, password); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Password reset for %s; their sessions were signed out.\n", u.Email)
	return nil
}

func fatal(msg string, err error) {
	slog.Error(msg, "err", err)
	os.Exit(1)
}
