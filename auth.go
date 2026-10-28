package main

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// Sign-in is in the app: anyone can create an account (unless server.signup is
// off), and a session cookie keeps a phone signed in for months. Quotes stay
// public; trips and saved places belong to a user. menged -user NAME resets a
// forgotten password from the server.

const (
	sessionCookie = "menged_session"
	sessionTTL    = 90 * 24 * time.Hour
	// A session used with less than this left is pushed out to a full sessionTTL,
	// so a phone in daily use never has to sign in again.
	sessionRenewBelow = 60 * 24 * time.Hour
	minPasswordLen    = 8
	maxPasswordLen    = 72 // bcrypt's limit
)

var (
	errBadLogin        = errors.New("wrong username or password")
	errInvalidUsername = errors.New("username must be 1–32 letters, digits, '.', '-' or '_'")
	errShortPassword   = errors.New("password must be at least 8 characters")
	errLongPassword    = errors.New("password must be at most 72 bytes")
	errUsernameTaken   = errors.New("that username is taken")
	errSignupClosed    = errors.New("sign-up is closed; ask the owner for an account")
)

// User is a signed-in driver.
type User struct {
	ID       int64  `json:"-"`
	Username string `json:"username"`
}

// dummyHash is compared against when the username doesn't exist, so a wrong
// username takes as long as a wrong password.
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("menged-no-such-user"), bcrypt.DefaultCost)

func validUsername(name string) bool {
	if name == "" || len(name) > 32 {
		return false
	}
	for _, r := range name {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' ||
			r == '.' || r == '-' || r == '_'
		if !ok {
			return false
		}
	}
	return true
}

// hashPassword checks username and password against the rules and returns the
// bcrypt hash to store.
func hashPassword(username, password string) (string, error) {
	switch {
	case !validUsername(username):
		return "", errInvalidUsername
	case len(password) < minPasswordLen:
		return "", errShortPassword
	case len(password) > maxPasswordLen:
		return "", errLongPassword
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(hash), err
}

// CreateUser adds a new user. Usernames are unique regardless of case.
func (s *Store) CreateUser(username, password string) (*User, error) {
	hash, err := hashPassword(username, password)
	if err != nil {
		return nil, err
	}
	res, err := s.db.Exec(`INSERT INTO users (username, password_hash, created_at) VALUES (?,?,?)`,
		username, hash, now())
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil, errUsernameTaken
		}
		return nil, err
	}
	id, err := res.LastInsertId()
	return &User{ID: id, Username: username}, err
}

// SetPassword creates username, or resets its password if it exists. A reset
// signs the user out everywhere.
func (s *Store) SetPassword(username, password string) (created bool, err error) {
	hash, err := hashPassword(username, password)
	if err != nil {
		return false, err
	}
	// One transaction, so a reset can't change the password yet leave old sessions valid.
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE users SET password_hash = ? WHERE username = ?`, hash, username)
	if err != nil {
		return false, err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		if _, err := tx.Exec(`DELETE FROM sessions
			WHERE user_id = (SELECT id FROM users WHERE username = ?)`, username); err != nil {
			return false, err
		}
	} else {
		if _, err := tx.Exec(`INSERT INTO users (username, password_hash, created_at) VALUES (?,?,?)`,
			username, hash, now()); err != nil {
			return false, err
		}
		created = true
	}
	return created, tx.Commit()
}

// Authenticate returns the user if username and password match.
func (s *Store) Authenticate(username, password string) (*User, error) {
	u := &User{}
	var hash string
	err := s.db.QueryRow(`SELECT id, username, password_hash FROM users WHERE username = ?`, username).
		Scan(&u.ID, &u.Username, &hash)
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

// CreateSession starts a session for userID and returns the cookie value. Only its
// hash is stored, so a copy of the database can't be used to sign in. Expired
// sessions are cleared out here, since nobody presents them again.
func (s *Store) CreateSession(userID int64) (token string, expires time.Time, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", time.Time{}, err
	}
	token = base64.RawURLEncoding.EncodeToString(b)
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
	err = s.db.QueryRow(`SELECT u.id, u.username, s.expires_at FROM sessions s
		JOIN users u ON u.id = s.user_id WHERE s.token_hash = ?`, th).Scan(&u.ID, &u.Username, &expiresAt)
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

// newSessionCookie is the cookie for value; logout sends the same cookie with MaxAge -1
// so the browser matches and removes it.
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

type loginRequest struct {
	Username string `json:"username"`
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

func (s *Server) signup(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.Server.Signup {
		writeError(w, http.StatusForbidden, errSignupClosed.Error())
		return
	}
	var req loginRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad json: "+err.Error())
		return
	}
	u, err := s.store.CreateUser(strings.TrimSpace(req.Username), req.Password)
	switch {
	case errors.Is(err, errUsernameTaken):
		writeError(w, http.StatusConflict, err.Error())
		return
	case errors.Is(err, errInvalidUsername), errors.Is(err, errShortPassword), errors.Is(err, errLongPassword):
		writeError(w, http.StatusBadRequest, err.Error())
		return
	case err != nil:
		s.serverError(w, "signup", err)
		return
	}
	s.startSession(w, r, u)
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad json: "+err.Error())
		return
	}
	u, err := s.store.Authenticate(strings.TrimSpace(req.Username), req.Password)
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
