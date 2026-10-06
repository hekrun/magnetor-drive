package auth

import (
	"testing"
	"time"
)

func TestLoginSessionAndThrottle(t *testing.T) {
	a, err := New("owner", "correct horse battery", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	a.now = func() time.Time { return now }

	tok, sess, ok := a.Login("1.1.1.1", "owner", "correct horse battery")
	if !ok || sess.CSRF == "" {
		t.Fatal("login should succeed")
	}
	if _, ok := a.Lookup(tok); !ok {
		t.Error("session should exist")
	}
	now = now.Add(2 * time.Hour)
	if _, ok := a.Lookup(tok); ok {
		t.Error("session should expire")
	}

	for i := 0; i < maxFailures; i++ {
		if _, _, ok := a.Login("2.2.2.2", "owner", "wrong"); ok {
			t.Fatal("wrong password accepted")
		}
	}
	if !a.Throttled("2.2.2.2") || a.Throttled("3.3.3.3") {
		t.Error("throttle should apply per client")
	}
	now = now.Add(failureWindow + time.Minute)
	if a.Throttled("2.2.2.2") {
		t.Error("throttle should lapse")
	}
	if _, _, ok := a.Login("x", "other", "correct horse battery"); ok {
		t.Error("wrong user accepted")
	}
	tok, _, _ = a.Login("x", "owner", "correct horse battery")
	a.Logout(tok)
	if _, ok := a.Lookup(tok); ok {
		t.Error("logout should invalidate")
	}
}
