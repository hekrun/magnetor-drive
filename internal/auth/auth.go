// Package auth provides single-owner password authentication, in-memory
// sessions, CSRF tokens and login throttling.
package auth

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"sync"
	"time"
)

const (
	pbkdf2Iterations = 600_000
	maxFailures      = 5
	failureWindow    = 15 * time.Minute
)

// Session is an authenticated browser session.
type Session struct {
	User    string
	CSRF    string
	Expires time.Time
}

// Authenticator verifies the owner's credentials and tracks sessions.
type Authenticator struct {
	user string
	salt []byte
	hash []byte
	ttl  time.Duration
	now  func() time.Time

	mu       sync.Mutex
	sessions map[string]*Session
	failures map[string][]time.Time
}

// New derives a password hash in memory; the plaintext is not retained.
func New(user, password string, ttl time.Duration) (*Authenticator, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	h, err := pbkdf2.Key(sha256.New, password, salt, pbkdf2Iterations, 32)
	if err != nil {
		return nil, err
	}
	return &Authenticator{
		user: user, salt: salt, hash: h, ttl: ttl, now: time.Now,
		sessions: map[string]*Session{}, failures: map[string][]time.Time{},
	}, nil
}

// Throttled reports whether client (e.g. an IP) has too many recent failures.
func (a *Authenticator) Throttled(client string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.recent(client)) >= maxFailures
}

func (a *Authenticator) recent(client string) []time.Time {
	cut := a.now().Add(-failureWindow)
	var keep []time.Time
	for _, t := range a.failures[client] {
		if t.After(cut) {
			keep = append(keep, t)
		}
	}
	if len(keep) == 0 {
		delete(a.failures, client)
	} else {
		a.failures[client] = keep
	}
	return keep
}

// Login checks credentials and returns a session token on success.
func (a *Authenticator) Login(client, user, password string) (string, *Session, bool) {
	// Always run the KDF so timing does not reveal whether the user matched.
	h, err := pbkdf2.Key(sha256.New, password, a.salt, pbkdf2Iterations, 32)
	okPass := err == nil && subtle.ConstantTimeCompare(h, a.hash) == 1
	okUser := subtle.ConstantTimeCompare([]byte(user), []byte(a.user)) == 1

	a.mu.Lock()
	defer a.mu.Unlock()
	if !okPass || !okUser {
		a.failures[client] = append(a.recent(client), a.now())
		return "", nil, false
	}
	delete(a.failures, client)
	tok := randomToken()
	s := &Session{User: a.user, CSRF: randomToken(), Expires: a.now().Add(a.ttl)}
	a.sessions[hashToken(tok)] = s
	a.gc()
	return tok, s, true
}

// Lookup returns the live session for a token.
func (a *Authenticator) Lookup(token string) (*Session, bool) {
	if token == "" {
		return nil, false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	k := hashToken(token)
	s, ok := a.sessions[k]
	if !ok {
		return nil, false
	}
	if !a.now().Before(s.Expires) {
		delete(a.sessions, k)
		return nil, false
	}
	return s, true
}

// Logout invalidates a session token.
func (a *Authenticator) Logout(token string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.sessions, hashToken(token))
}

func (a *Authenticator) gc() {
	now := a.now()
	for k, s := range a.sessions {
		if !now.Before(s.Expires) {
			delete(a.sessions, k)
		}
	}
}

// TTL is the session lifetime.
func (a *Authenticator) TTL() time.Duration { return a.ttl }

func randomToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func hashToken(t string) string {
	sum := sha256.Sum256([]byte(t))
	return hex.EncodeToString(sum[:])
}

// EqualToken compares two tokens in constant time.
func EqualToken(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
