package server

import (
	"testing"
	"time"
)

func TestStashHandsTheSecretOverExactlyOnce(t *testing.T) {
	s := newStash()

	id, err := s.put("someone@example.com", "glsa_secret")
	if err != nil {
		t.Fatalf("put: %v", err)
	}

	got, ok := s.take(id, "someone@example.com")
	if !ok || got != "glsa_secret" {
		t.Fatalf("take = %q, %v; want the secret", got, ok)
	}
	if _, ok := s.take(id, "someone@example.com"); ok {
		t.Error("the same link handed the secret over twice")
	}
}

func TestStashRefusesAnotherPerson(t *testing.T) {
	s := newStash()
	id, _ := s.put("someone@example.com", "glsa_secret")

	if _, ok := s.take(id, "nobody@example.com"); ok {
		t.Error("a stranger took the secret")
	}
	// The attempt burns the id rather than leaving it to be retried.
	if _, ok := s.take(id, "someone@example.com"); ok {
		t.Error("the id survived a failed attempt")
	}
}

func TestStashForgetsStaleSecrets(t *testing.T) {
	s := newStash()
	id, _ := s.put("someone@example.com", "glsa_secret")

	s.mu.Lock()
	v := s.items[id]
	v.until = time.Now().Add(-time.Second)
	s.items[id] = v
	s.mu.Unlock()

	if _, ok := s.take(id, "someone@example.com"); ok {
		t.Error("an expired secret was still handed over")
	}
}
