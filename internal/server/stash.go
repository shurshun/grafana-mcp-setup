package server

import (
	"crypto/rand"
	"encoding/base64"
	"sync"
	"time"
)

// How long a freshly issued secret waits for the browser to come and fetch it.
const stashTTL = 5 * time.Minute

// stash holds a secret between the POST that creates it and the redirect that
// shows it. Keeping the secret out of the POST response is what makes a refresh
// harmless: the shown page is a plain GET, so reloading it cannot rotate the
// token again.
//
// It lives in this process only, which is why the chart runs a single replica.
// With more, a redirect could land on another pod and the page would say the
// token is gone instead of showing it.
type stash struct {
	mu    sync.Mutex
	items map[string]stashed
}

type stashed struct {
	email string
	token string
	until time.Time
}

func newStash() *stash { return &stash{items: map[string]stashed{}} }

func (s *stash) put(email, token string) (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	id := base64.RawURLEncoding.EncodeToString(b)

	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for k, v := range s.items {
		if now.After(v.until) {
			delete(s.items, k)
		}
	}
	s.items[id] = stashed{email: email, token: token, until: now.Add(stashTTL)}
	return id, nil
}

// take reads the secret once. The email must match the person asking, so a
// guessed id cannot hand someone else's token over.
func (s *stash) take(id, email string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	v, ok := s.items[id]
	if !ok {
		return "", false
	}
	delete(s.items, id)
	if v.email != email || time.Now().After(v.until) {
		return "", false
	}
	return v.token, true
}
