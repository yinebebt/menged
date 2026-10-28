package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSetPasswordAndAuthenticate(t *testing.T) {
	s, _ := newTestStore(t) // has "driver" / "correct horse"

	if _, err := s.SetPassword("bad name", "long enough"); !errors.Is(err, errInvalidUsername) {
		t.Fatalf("invalid username: %v", err)
	}
	if _, err := s.SetPassword("abebe", "short"); !errors.Is(err, errShortPassword) {
		t.Fatalf("short password: %v", err)
	}
	if _, err := s.Authenticate("driver", "wrong password"); !errors.Is(err, errBadLogin) {
		t.Fatalf("wrong password: %v", err)
	}
	if _, err := s.Authenticate("nobody", "correct horse"); !errors.Is(err, errBadLogin) {
		t.Fatalf("unknown user: %v", err)
	}
	if u, err := s.Authenticate("DRIVER", "correct horse"); err != nil || u.Username != "driver" {
		t.Fatalf("usernames are case-insensitive: %v %v", u, err)
	}

	u, _ := s.Authenticate("driver", "correct horse")
	token, _, err := s.CreateSession(u.ID)
	if err != nil {
		t.Fatal(err)
	}
	created, err := s.SetPassword("Driver", "a new password")
	if err != nil || created {
		t.Fatalf("reset: created=%v err=%v", created, err)
	}
	if got, _, _ := s.SessionUser(token); got != nil {
		t.Fatal("a password reset must sign the user out")
	}
	if _, err := s.Authenticate("driver", "a new password"); err != nil {
		t.Fatalf("new password: %v", err)
	}
}

func TestSessionExpiryAndRenewal(t *testing.T) {
	s, uid := newTestStore(t)
	token, _, err := s.CreateSession(uid)
	if err != nil {
		t.Fatal(err)
	}
	setExpiry := func(d time.Duration) {
		t.Helper()
		if _, err := s.db.Exec(`UPDATE sessions SET expires_at = ?`,
			time.Now().UTC().Add(d).Format(time.RFC3339)); err != nil {
			t.Fatal(err)
		}
	}

	if u, renewed, err := s.SessionUser(token); err != nil || u == nil || !renewed.IsZero() {
		t.Fatalf("fresh session: u=%v renewed=%v err=%v", u, renewed, err)
	}
	setExpiry(10 * 24 * time.Hour)
	if u, renewed, _ := s.SessionUser(token); u == nil || time.Until(renewed) < 89*24*time.Hour {
		t.Fatalf("near expiry should renew: u=%v renewed=%v", u, renewed)
	}
	setExpiry(-time.Minute)
	if u, _, _ := s.SessionUser(token); u != nil {
		t.Fatal("expired session still valid")
	}

	// Expired sessions nobody presents again are pruned at the next sign-in.
	stale, _, _ := s.CreateSession(uid)
	_, _ = s.db.Exec(`UPDATE sessions SET expires_at = ? WHERE token_hash = ?`,
		time.Now().UTC().Add(-time.Hour).Format(time.RFC3339), hashToken(stale))
	if _, _, err := s.CreateSession(uid); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE token_hash = ?`, hashToken(stale)).Scan(&n)
	if n != 0 {
		t.Fatal("expired session not pruned")
	}
	if u, _, _ := s.SessionUser("not-a-token"); u != nil {
		t.Fatal("unknown token accepted")
	}
}

// client keeps the session cookie between requests, like a browser.
type client struct {
	t      *testing.T
	h      http.Handler
	cookie *http.Cookie
}

func (c *client) do(method, path, body string, hdr ...string) *httptest.ResponseRecorder {
	c.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Host = "menged.example"
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	if c.cookie != nil {
		req.AddCookie(c.cookie)
	}
	rec := httptest.NewRecorder()
	c.h.ServeHTTP(rec, req)
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == sessionCookie {
			if ck.MaxAge < 0 {
				c.cookie = nil
			} else {
				c.cookie = ck
			}
		}
	}
	return rec
}

func TestAuthHTTP(t *testing.T) {
	s, _ := newTestStore(t)
	addUser(t, s, "other")
	cfg := &Config{Fare: FareConfig{Currency: "ETB", BaseFare: 50, PricePerKM: 10, MinimumFare: 80}}
	h := NewServer(cfg, s, nil, nil).routes(http.NotFoundHandler())
	a := &client{t: t, h: h}
	b := &client{t: t, h: h}

	if rec := a.do("GET", "/api/trips", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("trips signed out: %d", rec.Code)
	}
	if rec := a.do("GET", "/api/me", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("me signed out: %d", rec.Code)
	}
	if rec := a.do("POST", "/api/login", `{"username":"driver","password":"nope"}`); rec.Code != http.StatusUnauthorized || a.cookie != nil {
		t.Fatalf("bad login: %d cookie=%v", rec.Code, a.cookie)
	}

	rec := a.do("POST", "/api/login", `{"username":"driver","password":"correct horse"}`,
		"X-Forwarded-Proto", "https")
	if rec.Code != http.StatusOK || a.cookie == nil {
		t.Fatalf("login: %d %s", rec.Code, rec.Body)
	}
	if !a.cookie.HttpOnly || !a.cookie.Secure || a.cookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("cookie flags: %+v", a.cookie)
	}
	if rec := a.do("GET", "/api/me", ""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"driver"`) {
		t.Fatalf("me: %d %s", rec.Code, rec.Body)
	}

	trip := `{"pickup_lat":9.01,"pickup_lng":38.76,"dest_lat":9.02,"dest_lng":38.77,
		"distance_km":3,"duration_min":10,"suggested_fare":100}`
	if rec := a.do("POST", "/api/trips", trip, "Origin", "https://menged.example"); rec.Code != http.StatusCreated {
		t.Fatalf("save trip: %d %s", rec.Code, rec.Body)
	}
	if rec := a.do("POST", "/api/trips", trip, "Origin", "https://evil.example"); rec.Code != http.StatusForbidden {
		t.Fatalf("cross-origin save: %d", rec.Code)
	}
	// A sibling subdomain is "same-site" to SameSite cookies, but not same-origin.
	if rec := a.do("POST", "/api/trips", trip, "Sec-Fetch-Site", "same-site"); rec.Code != http.StatusForbidden {
		t.Fatalf("same-site save: %d", rec.Code)
	}
	if rec := a.do("POST", "/api/places", `{"label":"Home","lat":9.01,"lng":38.76}`); rec.Code != http.StatusCreated {
		t.Fatalf("add place: %d %s", rec.Code, rec.Body)
	}

	// The other driver sees none of it, can't touch it, and can reuse the label.
	b.do("POST", "/api/login", `{"username":"other","password":"correct horse"}`)
	if rec := b.do("GET", "/api/trips", ""); rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("other's trips: %d %s", rec.Code, rec.Body)
	}
	if rec := b.do("DELETE", "/api/trips/1", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("other deleting a trip: %d", rec.Code)
	}
	if rec := b.do("PATCH", "/api/trips/1", `{"note":"mine now"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("other editing a trip: %d", rec.Code)
	}
	if rec := b.do("POST", "/api/places", `{"label":"Home","lat":9.02,"lng":38.77}`); rec.Code != http.StatusCreated {
		t.Fatalf("same label for another user: %d %s", rec.Code, rec.Body)
	}
	if rec := a.do("GET", "/api/trips", ""); !strings.Contains(rec.Body.String(), `"suggested_fare":100`) {
		t.Fatalf("owner's trip changed: %s", rec.Body)
	}

	if rec := a.do("POST", "/api/logout", ""); rec.Code != http.StatusNoContent || a.cookie != nil {
		t.Fatalf("logout: %d cookie=%v", rec.Code, a.cookie)
	}
	if rec := a.do("GET", "/api/trips", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("trips after logout: %d", rec.Code)
	}
}

func TestSignup(t *testing.T) {
	s, _ := newTestStore(t) // "driver" exists
	cfg := &Config{Server: ServerConfig{Signup: true}}
	h := NewServer(cfg, s, nil, nil).routes(http.NotFoundHandler())
	c := &client{t: t, h: h}

	cases := []struct {
		name, body string
		want       int
	}{
		{"taken, any case", `{"username":"DRIVER","password":"long enough"}`, http.StatusConflict},
		{"bad username", `{"username":"has space","password":"long enough"}`, http.StatusBadRequest},
		{"short password", `{"username":"abebe","password":"short"}`, http.StatusBadRequest},
		{"password over bcrypt's limit", `{"username":"abebe","password":"` + strings.Repeat("x", 73) + `"}`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		if rec := c.do("POST", "/api/signup", tc.body); rec.Code != tc.want {
			t.Fatalf("%s: %d %s", tc.name, rec.Code, rec.Body)
		}
	}
	if c.cookie != nil {
		t.Fatal("a failed sign-up must not sign in")
	}

	if rec := c.do("POST", "/api/signup", `{"username":" abebe ","password":"long enough"}`); rec.Code != http.StatusOK {
		t.Fatalf("signup: %d %s", rec.Code, rec.Body)
	}
	if rec := c.do("GET", "/api/me", ""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"abebe"`) {
		t.Fatalf("signed in after sign-up: %d %s", rec.Code, rec.Body)
	}
	if _, err := s.Authenticate("abebe", "long enough"); err != nil {
		t.Fatalf("new account can sign in: %v", err)
	}

	cfg.Server.Signup = false
	if rec := c.do("POST", "/api/signup", `{"username":"closed","password":"long enough"}`); rec.Code != http.StatusForbidden {
		t.Fatalf("closed sign-up: %d", rec.Code)
	}
}
