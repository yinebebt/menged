package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"net/mail"
	"net/url"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// Sign-in is in the app, by email and password. Anyone can create an account
// (unless server.signup is off), a forgotten password is reset by an emailed link,
// and a session cookie keeps a phone signed in for months. Quotes stay public;
// trips and saved places belong to a user.

const (
	sessionCookie = "menged_session"
	sessionTTL    = 90 * 24 * time.Hour
	// A session used with less than this left is pushed out to a full sessionTTL,
	// so a phone in daily use never has to sign in again.
	sessionRenewBelow = 60 * 24 * time.Hour
	resetTTL          = time.Hour
	minPasswordLen    = 8
	maxPasswordLen    = 72 // bcrypt's limit
	maxEmailLen       = 254
)

var (
	errBadLogin      = errors.New("wrong email or password")
	errInvalidEmail  = errors.New("that doesn't look like an email address")
	errShortPassword = errors.New("password must be at least 8 characters")
	errLongPassword  = errors.New("password must be at most 72 bytes")
	errEmailTaken    = errors.New("an account with that email already exists; sign in or reset the password")
	errSignupClosed  = errors.New("sign-up is closed; ask the owner for an account")
	errBadResetLink  = errors.New("this reset link has expired or was already used; ask for a new one")
)

// User is a signed-in driver.
type User struct {
	ID    int64  `json:"-"`
	Email string `json:"email"`
}

// dummyHash is compared against when the email has no account, so an unknown
// email takes as long as a wrong password.
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("menged-no-such-user"), bcrypt.DefaultCost)

// normalizeEmail trims and lower-cases a bare address ("a@b.co", no display name),
// or returns errInvalidEmail.
func normalizeEmail(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	a, err := mail.ParseAddress(s)
	if err != nil || a.Address != s || len(s) > maxEmailLen || !strings.Contains(s[strings.LastIndex(s, "@"):], ".") {
		return "", errInvalidEmail
	}
	return s, nil
}

func checkPassword(password string) error {
	switch {
	case len(password) < minPasswordLen:
		return errShortPassword
	case len(password) > maxPasswordLen:
		return errLongPassword
	}
	return nil
}

func hashPassword(password string) (string, error) {
	if err := checkPassword(password); err != nil {
		return "", err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(hash), err
}

// CreateUser adds a new user. Emails are unique regardless of case.
func (s *Store) CreateUser(email, password string) (*User, error) {
	email, err := normalizeEmail(email)
	if err != nil {
		return nil, err
	}
	hash, err := hashPassword(password)
	if err != nil {
		return nil, err
	}
	res, err := s.db.Exec(`INSERT INTO users (email, password_hash, created_at) VALUES (?,?,?)`,
		email, hash, now())
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil, errEmailTaken
		}
		return nil, err
	}
	id, err := res.LastInsertId()
	return &User{ID: id, Email: email}, err
}

// SetPassword replaces userID's password and signs them out everywhere, in one
// transaction so a reset can't change the password yet leave old sessions valid.
func (s *Store) SetPassword(userID int64, password string) error {
	hash, err := hashPassword(password)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE users SET password_hash = ? WHERE id = ?`, hash, userID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM sessions WHERE user_id = ?`, userID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM password_resets WHERE user_id = ?`, userID); err != nil {
		return err
	}
	return tx.Commit()
}

// UserByEmail returns the user with that email, or nil.
func (s *Store) UserByEmail(email string) (*User, error) {
	email, err := normalizeEmail(email)
	if err != nil {
		return nil, nil
	}
	u := &User{}
	err = s.db.QueryRow(`SELECT id, email FROM users WHERE email = ?`, email).Scan(&u.ID, &u.Email)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return u, err
}

// Authenticate returns the user if email and password match.
func (s *Store) Authenticate(email, password string) (*User, error) {
	u := &User{}
	var hash string
	norm, _ := normalizeEmail(email)
	err := s.db.QueryRow(`SELECT id, email, password_hash FROM users WHERE email = ?`, norm).
		Scan(&u.ID, &u.Email, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(password))
		return nil, errBadLogin
	}
	if err != nil {
		return nil, err
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		return nil, errBadLogin
	}
	return u, nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// newToken is a random URL-safe token. Only its hash is stored (sessions, reset
// links), so a copy of the database can't be used to sign in.
func newToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// CreateSession starts a session for userID and returns the cookie value. Expired
// sessions are cleared out here, since nobody presents them again.
func (s *Store) CreateSession(userID int64) (token string, expires time.Time, err error) {
	if token, err = newToken(); err != nil {
		return "", time.Time{}, err
	}
	now := time.Now().UTC()
	expires = now.Add(sessionTTL)
	// expires_at is always RFC3339 in UTC, so string order is time order.
	if _, err := s.db.Exec(`DELETE FROM sessions WHERE expires_at < ?`, now.Format(time.RFC3339)); err != nil {
		return "", time.Time{}, err
	}
	_, err = s.db.Exec(`INSERT INTO sessions (token_hash, user_id, created_at, expires_at) VALUES (?,?,?,?)`,
		hashToken(token), userID, now.Format(time.RFC3339), expires.Format(time.RFC3339))
	return token, expires, err
}

// SessionUser returns the user behind a session token, or nil if the token is
// unknown or expired. renewed is the new expiry when the session was extended.
func (s *Store) SessionUser(token string) (u *User, renewed time.Time, err error) {
	th := hashToken(token)
	u = &User{}
	var expiresAt string
	err = s.db.QueryRow(`SELECT u.id, u.email, s.expires_at FROM sessions s
		JOIN users u ON u.id = s.user_id WHERE s.token_hash = ?`, th).Scan(&u.ID, &u.Email, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, time.Time{}, nil
	}
	if err != nil {
		return nil, time.Time{}, err
	}
	expires, err := time.Parse(time.RFC3339, expiresAt)
	if err != nil {
		return nil, time.Time{}, err
	}
	left := time.Until(expires)
	if left <= 0 {
		_, err = s.db.Exec(`DELETE FROM sessions WHERE token_hash = ?`, th)
		return nil, time.Time{}, err
	}
	if left < sessionRenewBelow {
		renewed = time.Now().UTC().Add(sessionTTL)
		if _, err := s.db.Exec(`UPDATE sessions SET expires_at = ? WHERE token_hash = ?`,
			renewed.Format(time.RFC3339), th); err != nil {
			return nil, time.Time{}, err
		}
	}
	return u, renewed, nil
}

// DeleteSession signs a session out. Unknown tokens are not an error.
func (s *Store) DeleteSession(token string) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE token_hash = ?`, hashToken(token))
	return err
}

// CreateReset issues a password-reset token for userID, valid for resetTTL.
func (s *Store) CreateReset(userID int64) (string, error) {
	token, err := newToken()
	if err != nil {
		return "", err
	}
	now := time.Now().UTC()
	if _, err := s.db.Exec(`DELETE FROM password_resets WHERE expires_at < ?`, now.Format(time.RFC3339)); err != nil {
		return "", err
	}
	_, err = s.db.Exec(`INSERT INTO password_resets (token_hash, user_id, expires_at) VALUES (?,?,?)`,
		hashToken(token), userID, now.Add(resetTTL).Format(time.RFC3339))
	return token, err
}

// UseReset sets a new password with a reset token. The token works once; using it
// also signs the user out everywhere and voids their other reset links.
func (s *Store) UseReset(token, password string) (*User, error) {
	if err := checkPassword(password); err != nil {
		return nil, err
	}
	u := &User{}
	err := s.db.QueryRow(`SELECT u.id, u.email FROM password_resets r JOIN users u ON u.id = r.user_id
		WHERE r.token_hash = ? AND r.expires_at > ?`, hashToken(token), time.Now().UTC().Format(time.RFC3339)).
		Scan(&u.ID, &u.Email)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errBadResetLink
	}
	if err != nil {
		return nil, err
	}
	return u, s.SetPassword(u.ID, password)
}

// newSessionCookie is the cookie for value; logout sends the same cookie with
// MaxAge -1 so the browser matches and removes it.
func newSessionCookie(r *http.Request, value string, expires time.Time) *http.Cookie {
	return &http.Cookie{
		Name:     sessionCookie,
		Value:    value,
		Path:     "/",
		Expires:  expires,
		HttpOnly: true,
		Secure:   isHTTPS(r),
		SameSite: http.SameSiteLaxMode,
	}
}

// isHTTPS is true behind the TLS-terminating proxy (or on direct TLS). On plain
// http://127.0.0.1 during development the cookie must not be Secure.
func isHTTPS(r *http.Request) bool {
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
}

// requireUser resolves the session cookie and answers 401 when it is missing or
// expired. Only personal endpoints use it, so static files and public quotes never
// touch the sessions table. Cross-site requests are refused before this, by
// http.CrossOriginProtection in routes.
func (s *Server) requireUser(next func(http.ResponseWriter, *http.Request, *User)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var u *User
		if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
			var renewed time.Time
			if u, renewed, err = s.store.SessionUser(c.Value); err != nil {
				s.serverError(w, "session", err)
				return
			}
			if u != nil && !renewed.IsZero() {
				http.SetCookie(w, newSessionCookie(r, c.Value, renewed))
			}
		}
		if u == nil {
			writeError(w, http.StatusUnauthorized, "sign in required")
			return
		}
		next(w, r, u)
	}
}

type credentials struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// startSession signs u in on this browser and answers with the user.
func (s *Server) startSession(w http.ResponseWriter, r *http.Request, u *User) {
	token, expires, err := s.store.CreateSession(u.ID)
	if err != nil {
		s.serverError(w, "create session", err)
		return
	}
	http.SetCookie(w, newSessionCookie(r, token, expires))
	writeJSON(w, http.StatusOK, u)
}

// userError answers the validation errors a person can fix; ok is false otherwise.
func userError(w http.ResponseWriter, err error) (ok bool) {
	switch {
	case errors.Is(err, errEmailTaken):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, errInvalidEmail), errors.Is(err, errShortPassword),
		errors.Is(err, errLongPassword), errors.Is(err, errBadResetLink):
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		return false
	}
	return true
}

func (s *Server) signup(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.Server.Signup {
		writeError(w, http.StatusForbidden, errSignupClosed.Error())
		return
	}
	var req credentials
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad json: "+err.Error())
		return
	}
	u, err := s.store.CreateUser(req.Email, req.Password)
	if err != nil {
		if !userError(w, err) {
			s.serverError(w, "signup", err)
		}
		return
	}
	s.startSession(w, r, u)
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var req credentials
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad json: "+err.Error())
		return
	}
	u, err := s.store.Authenticate(req.Email, req.Password)
	if errors.Is(err, errBadLogin) {
		writeError(w, http.StatusUnauthorized, err.Error())
		return
	}
	if err != nil {
		s.serverError(w, "login", err)
		return
	}
	s.startSession(w, r, u)
}

// forgotPassword emails a reset link if the address has an account. The answer is
// the same either way, and the email is sent in the background.
func (s *Server) forgotPassword(w http.ResponseWriter, r *http.Request) {
	var req credentials
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad json: "+err.Error())
		return
	}
	if _, err := normalizeEmail(req.Email); err != nil {
		userError(w, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)

	u, err := s.store.UserByEmail(req.Email)
	if err != nil {
		slog.Error("password reset lookup", "err", err)
		return
	}
	if u == nil {
		return
	}
	token, err := s.store.CreateReset(u.ID)
	if err != nil {
		slog.Error("create password reset", "err", err)
		return
	}
	link := s.cfg.Server.PublicURL + "/?reset=" + url.QueryEscape(token)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := s.mailer.Send(ctx, u.Email, "Reset your menged password", resetEmail(link)); err != nil {
			slog.Error("send password reset", "err", err)
		}
	}()
}

func resetEmail(link string) string {
	return "Someone asked to reset the password for your menged account.\n\n" +
		"Open this link within an hour to choose a new one:\n\n" + link + "\n\n" +
		"If it wasn't you, ignore this email; your password stays the same.\n"
}

type resetRequest struct {
	Token    string `json:"token"`
	Password string `json:"password"`
}

// resetPassword sets a new password from an emailed link. Every session ends,
// this browser's too, so the new password is used once right away; the answer
// carries the email to fill in the sign-in form.
func (s *Server) resetPassword(w http.ResponseWriter, r *http.Request) {
	var req resetRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad json: "+err.Error())
		return
	}
	u, err := s.store.UseReset(req.Token, req.Password)
	if err != nil {
		if !userError(w, err) {
			s.serverError(w, "reset password", err)
		}
		return
	}
	writeJSON(w, http.StatusOK, u)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
		if err := s.store.DeleteSession(c.Value); err != nil {
			s.serverError(w, "logout", err)
			return
		}
	}
	c := newSessionCookie(r, "", time.Time{})
	c.MaxAge = -1
	http.SetCookie(w, c)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) me(w http.ResponseWriter, r *http.Request, u *User) {
	writeJSON(w, http.StatusOK, u)
}
