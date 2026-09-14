package server

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"time"
)

const (
	defaultSessionTTL    = time.Hour
	maxSessionTTLSeconds = int64(24 * 60 * 60)
	oauthStateTTL        = 10 * time.Minute
	maxSessionCookie     = 3800

	sessionCookieName = "grafana_mcp_session"
	stateCookieName   = "grafana_mcp_oauth_state"
)

type sessionPayload struct {
	Version int    `json:"v"`
	IDToken string `json:"id_token"`
	Expires int64  `json:"exp"`
}

type oauthStatePayload struct {
	Version      int    `json:"v"`
	State        string `json:"state"`
	Nonce        string `json:"nonce"`
	CodeVerifier string `json:"code_verifier"`
	Expires      int64  `json:"exp"`
}

type sessionCodec struct {
	aead   cipher.AEAD
	aad    []byte
	now    func() time.Time
	random io.Reader
}

func newSessionCodec(key []byte, basePath string) (*sessionCodec, error) {
	if len(key) != 32 {
		return nil, errors.New("session cookie key must contain 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &sessionCodec{
		aead:   aead,
		aad:    []byte("grafana-mcp-native-oidc-v1\x00" + basePath),
		now:    time.Now,
		random: rand.Reader,
	}, nil
}

func (c *sessionCodec) seal(value any, kind string) (string, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(c.random, nonce); err != nil {
		return "", err
	}
	sealed := c.aead.Seal(nonce, nonce, payload, c.associatedData(kind))
	encoded := base64.RawURLEncoding.EncodeToString(sealed)
	if len(encoded) > maxSessionCookie {
		return "", errors.New("native session exceeds the cookie size limit")
	}
	return encoded, nil
}

func (c *sessionCodec) open(value, kind string, out any) error {
	if value == "" || len(value) > maxSessionCookie {
		return errors.New("invalid native session cookie")
	}
	sealed, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(sealed) < c.aead.NonceSize() {
		return errors.New("invalid native session cookie")
	}
	nonce, ciphertext := sealed[:c.aead.NonceSize()], sealed[c.aead.NonceSize():]
	plain, err := c.aead.Open(nil, nonce, ciphertext, c.associatedData(kind))
	if err != nil {
		return errors.New("invalid native session cookie")
	}
	if err := json.Unmarshal(plain, out); err != nil {
		return errors.New("invalid native session cookie")
	}
	return nil
}

func (c *sessionCodec) associatedData(kind string) []byte {
	data := make([]byte, 0, len(c.aad)+1+len(kind))
	data = append(data, c.aad...)
	data = append(data, 0)
	return append(data, kind...)
}

func (c *sessionCodec) sealSession(idToken string, expires time.Time) (string, error) {
	if idToken == "" || expires.IsZero() {
		return "", errors.New("native session is incomplete")
	}
	return c.seal(sessionPayload{Version: 1, IDToken: idToken, Expires: expires.Unix()}, "session")
}

func (c *sessionCodec) openSession(value string) (sessionPayload, error) {
	var payload sessionPayload
	if err := c.open(value, "session", &payload); err != nil {
		return sessionPayload{}, err
	}
	if payload.Version != 1 || payload.IDToken == "" || payload.Expires <= c.now().Unix() {
		return sessionPayload{}, errors.New("native session expired or invalid")
	}
	return payload, nil
}

func (c *sessionCodec) sealState(state, nonce, codeVerifier string) (string, error) {
	if state == "" || nonce == "" || codeVerifier == "" {
		return "", errors.New("native OAuth state is incomplete")
	}
	return c.seal(oauthStatePayload{
		Version:      1,
		State:        state,
		Nonce:        nonce,
		CodeVerifier: codeVerifier,
		Expires:      c.now().Add(oauthStateTTL).Unix(),
	}, "oauth-state")
}

func (c *sessionCodec) openState(value string) (oauthStatePayload, error) {
	var payload oauthStatePayload
	if err := c.open(value, "oauth-state", &payload); err != nil {
		return oauthStatePayload{}, err
	}
	if payload.Version != 1 || payload.State == "" || payload.Nonce == "" || payload.CodeVerifier == "" || payload.Expires <= c.now().Unix() {
		return oauthStatePayload{}, errors.New("native OAuth state expired or invalid")
	}
	return payload, nil
}
