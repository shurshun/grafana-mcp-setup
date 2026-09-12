package server

import (
	"bytes"
	"testing"
	"time"
)

func testFlash(t *testing.T) *flashCodec {
	t.Helper()
	c, err := newFlashCodec(bytes.Repeat([]byte{7}, 32), 5*time.Minute, "/setup-mcp")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestFlashCookieEncryptsAndBindsTheSecret(t *testing.T) {
	c := testFlash(t)
	sealed, err := c.seal("someone@example.com", "glsa_secret")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains([]byte(sealed), []byte("glsa_secret")) {
		t.Fatal("sealed cookie contains the plaintext token")
	}
	got, err := c.open(sealed, "someone@example.com")
	if err != nil || got.Token != "glsa_secret" {
		t.Fatalf("open = %#v, %v", got, err)
	}
	if _, err := c.open(sealed, "nobody@example.com"); err == nil {
		t.Fatal("another identity opened the cookie")
	}
}

func TestFlashCookieExpiresServerSide(t *testing.T) {
	c := testFlash(t)
	now := time.Unix(1_700_000_000, 0)
	c.now = func() time.Time { return now }
	sealed, err := c.seal("someone@example.com", "glsa_secret")
	if err != nil {
		t.Fatal(err)
	}
	c.now = func() time.Time { return now.Add(5 * time.Minute) }
	if _, err := c.open(sealed, "someone@example.com"); err == nil {
		t.Fatal("expired cookie opened")
	}
}

func TestCSRFTokenBindsIdentityAndExpiry(t *testing.T) {
	c := testFlash(t)
	now := time.Unix(1_700_000_000, 0)
	c.now = func() time.Time { return now }
	token, err := c.csrf("someone@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.validateCSRF(token, "someone@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := c.validateCSRF(token, "nobody@example.com"); err == nil {
		t.Fatal("CSRF token accepted for another identity")
	}
	c.now = func() time.Time { return now.Add(5 * time.Minute) }
	if err := c.validateCSRF(token, "someone@example.com"); err == nil {
		t.Fatal("expired CSRF token accepted")
	}
}
