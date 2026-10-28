package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNormalizeEmail(t *testing.T) {
	for in, want := range map[string]string{
		" Abebe@Example.COM ": "abebe@example.com",
		"a.b+menged@mail.co":  "a.b+menged@mail.co",
	} {
		if got, err := normalizeEmail(in); err != nil || got != want {
			t.Errorf("%q: got %q, %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "abebe", "abebe@", "@example.com", "abebe@localhost",
		"Abebe <abebe@example.com>", "a b@example.com", strings.Repeat("a", 250) + "@example.com"} {
		if _, err := normalizeEmail(bad); !errors.Is(err, errInvalidEmail) {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestCreateUserAndAuthenticate(t *testing.T) {
	s, _ := newTestStore(t) // driver@example.com / "correct horse"

	if _, err := s.CreateUser("not-an-email", "long enough"); !errors.Is(err, errInvalidEmail) {
		t.Fatalf("invalid email: %v", err)
	}
	if _, err := s.CreateUser("abebe@example.com", "short"); !errors.Is(err, errShortPassword) {
		t.Fatalf("short password: %v", err)
	}
	if _, err := s.CreateUser("abebe@example.com", strings.Repeat("x", 73)); !errors.Is(err, errLongPassword) {
		t.Fatalf("long password: %v", err)
	}
	if _, err := s.CreateUser("DRIVER@example.com", "long enough"); !errors.Is(err, errEmailTaken) {
		t.Fatalf("taken, any case: %v", err)
	}
	if _, err := s.Authenticate("driver@example.com", "wrong password"); !errors.Is(err, errBadLogin) {
		t.Fatalf("wrong password: %v", err)
	}
	if _, err := s.Authenticate("nobody@example.com", "correct horse"); !errors.Is(err, errBadLogin) {
		t.Fatalf("unknown email: %v", err)
	}
	if u, err := s.Authenticate(" Driver@Example.com", "correct horse"); err != nil || u.Email != "driver@example.com" {
		t.Fatalf("email is case-insensitive: %v %v", u, err)
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
	if u, _, _ := s.SessionUser("not-a-token"); u != nil {
		t.Fatal("unknown token accepted")
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

const driverLogin = `{"email":"driver@example.com","password":"correct horse"}`

func TestAuthHTTP(t *testing.T) {
	s, _ := newTestStore(t)
	addUser(t, s, "other@example.com")
	cfg := &Config{Fare: FareConfig{Currency: "ETB", BaseFare: 50, PricePerKM: 10, MinimumFare: 80}}
	h := NewServer(cfg, s, nil, nil, logMailer{}).routes(http.NotFoundHandler())
	a := &client{t: t, h: h}
	b := &client{t: t, h: h}

	if rec := a.do("GET", "/api/trips", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("trips signed out: %d", rec.Code)
	}
	if rec := a.do("GET", "/api/me", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("me signed out: %d", rec.Code)
	}
	if rec := a.do("POST", "/api/login", `{"email":"driver@example.com","password":"nope"}`); rec.Code != http.StatusUnauthorized || a.cookie != nil {
		t.Fatalf("bad login: %d cookie=%v", rec.Code, a.cookie)
	}

	rec := a.do("POST", "/api/login", driverLogin, "X-Forwarded-Proto", "https")
	if rec.Code != http.StatusOK || a.cookie == nil {
		t.Fatalf("login: %d %s", rec.Code, rec.Body)
	}
	if !a.cookie.HttpOnly || !a.cookie.Secure || a.cookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("cookie flags: %+v", a.cookie)
	}
	if rec := a.do("GET", "/api/me", ""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"driver@example.com"`) {
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
	b.do("POST", "/api/login", `{"email":"other@example.com","password":"correct horse"}`)
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
	s, _ := newTestStore(t) // driver@example.com exists
	cfg := &Config{Server: ServerConfig{Signup: true}}
	h := NewServer(cfg, s, nil, nil, logMailer{}).routes(http.NotFoundHandler())
	c := &client{t: t, h: h}

	cases := []struct {
		name, body string
		want       int
	}{
		{"taken, any case", `{"email":"DRIVER@example.com","password":"long enough"}`, http.StatusConflict},
		{"not an email", `{"email":"abebe","password":"long enough"}`, http.StatusBadRequest},
		{"short password", `{"email":"abebe@example.com","password":"short"}`, http.StatusBadRequest},
		{"password over bcrypt's limit", `{"email":"abebe@example.com","password":"` + strings.Repeat("x", 73) + `"}`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		if rec := c.do("POST", "/api/signup", tc.body); rec.Code != tc.want {
			t.Fatalf("%s: %d %s", tc.name, rec.Code, rec.Body)
		}
	}
	if c.cookie != nil {
		t.Fatal("a failed sign-up must not sign in")
	}

	if rec := c.do("POST", "/api/signup", `{"email":" Abebe@Example.com ","password":"long enough"}`); rec.Code != http.StatusOK {
		t.Fatalf("signup: %d %s", rec.Code, rec.Body)
	}
	if rec := c.do("GET", "/api/me", ""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"abebe@example.com"`) {
		t.Fatalf("signed in after sign-up: %d %s", rec.Code, rec.Body)
	}

	cfg.Server.Signup = false
	if rec := c.do("POST", "/api/signup", `{"email":"closed@example.com","password":"long enough"}`); rec.Code != http.StatusForbidden {
		t.Fatalf("closed sign-up: %d", rec.Code)
	}
}

// captureMailer records sent emails so tests can follow the reset link.
type captureMailer struct {
	mu   sync.Mutex
	sent []string // "to|text"
	got  chan struct{}
}

func (m *captureMailer) Send(_ context.Context, to, _, text string) error {
	m.mu.Lock()
	m.sent = append(m.sent, to+"|"+text)
	m.mu.Unlock()
	m.got <- struct{}{}
	return nil
}

var resetLink = regexp.MustCompile(`https://menged\.example/\?reset=(\S+)`)

func TestPasswordReset(t *testing.T) {
	s, uid := newTestStore(t)
	mailer := &captureMailer{got: make(chan struct{}, 4)}
	cfg := &Config{Server: ServerConfig{PublicURL: "https://menged.example"}}
	h := NewServer(cfg, s, nil, nil, mailer).routes(http.NotFoundHandler())
	phone := &client{t: t, h: h}
	laptop := &client{t: t, h: h}
	phone.do("POST", "/api/login", driverLogin)

	// Unknown and known addresses get the same answer; only a real account gets mail.
	if rec := laptop.do("POST", "/api/password/forgot", `{"email":"nobody@example.com"}`); rec.Code != http.StatusAccepted {
		t.Fatalf("forgot, unknown: %d", rec.Code)
	}
	if rec := laptop.do("POST", "/api/password/forgot", `{"email":"Driver@Example.com"}`); rec.Code != http.StatusAccepted {
		t.Fatalf("forgot, known: %d", rec.Code)
	}
	select {
	case <-mailer.got:
	case <-time.After(5 * time.Second):
		t.Fatal("no reset email sent")
	}
	mailer.mu.Lock()
	sent := append([]string(nil), mailer.sent...)
	mailer.mu.Unlock()
	if len(sent) != 1 || !strings.HasPrefix(sent[0], "driver@example.com|") {
		t.Fatalf("sent: %q", sent)
	}
	m := resetLink.FindStringSubmatch(sent[0])
	if m == nil {
		t.Fatalf("no link in %q", sent[0])
	}
	token, _ := url.QueryUnescape(m[1])

	if rec := laptop.do("POST", "/api/password/reset", `{"token":"`+token+`","password":"short"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("short new password: %d", rec.Code)
	}
	if rec := laptop.do("POST", "/api/password/reset", `{"token":"`+token+`","password":"a new password"}`); rec.Code != http.StatusOK || laptop.cookie == nil {
		t.Fatalf("reset: %d %s", rec.Code, rec.Body)
	}
	if rec := phone.do("GET", "/api/me", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("a reset must sign other devices out: %d", rec.Code)
	}
	if _, err := s.Authenticate("driver@example.com", "a new password"); err != nil {
		t.Fatalf("new password: %v", err)
	}
	if rec := laptop.do("POST", "/api/password/reset", `{"token":"`+token+`","password":"another password"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("link reused: %d", rec.Code)
	}

	expired, _ := s.CreateReset(uid)
	_, _ = s.db.Exec(`UPDATE password_resets SET expires_at = ?`, time.Now().UTC().Add(-time.Minute).Format(time.RFC3339))
	if _, err := s.UseReset(expired, "yet another one"); !errors.Is(err, errBadResetLink) {
		t.Fatalf("expired link: %v", err)
	}
}
