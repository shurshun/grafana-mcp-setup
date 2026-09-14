package server

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func testSessionCodec(t *testing.T) *sessionCodec {
	t.Helper()
	c, err := newSessionCodec(bytes.Repeat([]byte{9}, 32), "/setup-mcp")
	if err != nil {
		t.Fatal(err)
	}
	c.now = func() time.Time { return time.Unix(1_700_000_000, 0) }
	return c
}

func TestNativeSessionIsEncryptedBoundToKeyAndExpires(t *testing.T) {
	c := testSessionCodec(t)
	sealed, err := c.sealSession("signed.id.token", c.now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sealed, "signed.id.token") {
		t.Fatal("native session cookie contains the ID token")
	}
	opened, err := c.openSession(sealed)
	if err != nil || opened.IDToken != "signed.id.token" {
		t.Fatalf("openSession = %#v, %v", opened, err)
	}
	other, err := newSessionCodec(bytes.Repeat([]byte{8}, 32), "/setup-mcp")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.openSession(sealed); err == nil {
		t.Fatal("session opened with another key")
	}
	c.now = func() time.Time { return time.Unix(1_700_000_000, 0).Add(time.Hour) }
	if _, err := c.openSession(sealed); err == nil {
		t.Fatal("expired session opened")
	}
}

func TestNativeOAuthStateBindsStateNonceAndPKCE(t *testing.T) {
	c := testSessionCodec(t)
	sealed, err := c.sealState("state", "nonce", "code-verifier")
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.openState(sealed)
	if err != nil || got.State != "state" || got.Nonce != "nonce" || got.CodeVerifier != "code-verifier" {
		t.Fatalf("openState = %#v, %v", got, err)
	}
	c.now = func() time.Time { return time.Unix(1_700_000_000, 0).Add(oauthStateTTL) }
	if _, err := c.openState(sealed); err == nil {
		t.Fatal("expired OAuth state opened")
	}
}

func TestNativeSessionCookieSizeIsBounded(t *testing.T) {
	c := testSessionCodec(t)
	if _, err := c.sealSession(strings.Repeat("x", 5000), c.now().Add(time.Hour)); err == nil {
		t.Fatal("oversized native session accepted")
	}
}
