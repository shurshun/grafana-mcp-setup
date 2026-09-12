package server

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

const flashCookieName = "grafana_mcp_flash"

type flashCodec struct {
	aead    cipher.AEAD
	csrfKey [32]byte
	ttl     time.Duration
	now     func() time.Time
	random  io.Reader
	aad     []byte
}

type flashPayload struct {
	Version      int    `json:"v"`
	Email        string `json:"email"`
	Token        string `json:"token"`
	Expires      int64  `json:"exp"`
	TokenCreated int64  `json:"tokenCreated"`
	TokenExpires int64  `json:"tokenExpires"`
	Partial      bool   `json:"partial,omitempty"`
}

type flashPreparation struct {
	nonce   []byte
	expires int64
}

type openedFlash struct {
	Token        string
	Partial      bool
	TokenCreated time.Time
	TokenExpires *time.Time
}

type deliveryPreparation struct {
	success flashPreparation
	partial flashPreparation
}

func newFlashCodec(key []byte, ttl time.Duration, basePath string) (*flashCodec, error) {
	if len(key) != 32 {
		return nil, errors.New("flash cookie key must contain 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &flashCodec{
		aead:    aead,
		csrfKey: sha256.Sum256(append(append([]byte(nil), key...), []byte("csrf-v1")...)),
		ttl:     ttl,
		now:     time.Now,
		random:  rand.Reader,
		aad:     []byte("grafana-mcp-flash-v1\x00" + basePath),
	}, nil
}

func (c *flashCodec) prepare() (flashPreparation, error) {
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(c.random, nonce); err != nil {
		return flashPreparation{}, err
	}
	return flashPreparation{nonce: nonce, expires: c.now().Add(c.ttl).Unix()}, nil
}

func (c *flashCodec) prepareDelivery() (deliveryPreparation, error) {
	success, err := c.prepare()
	if err != nil {
		return deliveryPreparation{}, err
	}
	partial, err := c.prepare()
	if err != nil {
		return deliveryPreparation{}, err
	}
	return deliveryPreparation{success: success, partial: partial}, nil
}

func (c *flashCodec) sealPrepared(prepared flashPreparation, email, token string, partial bool, created time.Time, tokenExpires *time.Time) string {
	expires := int64(0)
	if tokenExpires != nil {
		expires = tokenExpires.Unix()
	}
	payload, _ := json.Marshal(flashPayload{
		Version:      1,
		Email:        email,
		Token:        token,
		Expires:      prepared.expires,
		TokenCreated: created.Unix(),
		TokenExpires: expires,
		Partial:      partial,
	})
	sealed := c.aead.Seal(prepared.nonce, prepared.nonce, payload, c.aad)
	return base64.RawURLEncoding.EncodeToString(sealed)
}

func (c *flashCodec) seal(email, token string) (string, error) {
	prepared, err := c.prepare()
	if err != nil {
		return "", err
	}
	return c.sealPrepared(prepared, email, token, false, c.now(), nil), nil
}

func (c *flashCodec) open(value, email string) (openedFlash, error) {
	sealed, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(sealed) < c.aead.NonceSize() {
		return openedFlash{}, errors.New("invalid flash cookie")
	}
	nonce, ciphertext := sealed[:c.aead.NonceSize()], sealed[c.aead.NonceSize():]
	plain, err := c.aead.Open(nil, nonce, ciphertext, c.aad)
	if err != nil {
		return openedFlash{}, errors.New("invalid flash cookie")
	}
	var payload flashPayload
	if err := json.Unmarshal(plain, &payload); err != nil {
		return openedFlash{}, errors.New("invalid flash cookie")
	}
	if payload.Version != 1 || payload.Token == "" || subtle.ConstantTimeCompare([]byte(payload.Email), []byte(email)) != 1 {
		return openedFlash{}, errors.New("flash cookie belongs to another identity")
	}
	if c.now().Unix() >= payload.Expires {
		return openedFlash{}, errors.New("flash cookie expired")
	}
	opened := openedFlash{Token: payload.Token, Partial: payload.Partial, TokenCreated: time.Unix(payload.TokenCreated, 0).UTC()}
	opened.TokenExpires = unixTime(payload.TokenExpires)
	return opened, nil
}

func (c *flashCodec) csrf(email string) (string, error) {
	nonce := make([]byte, 16)
	if _, err := io.ReadFull(c.random, nonce); err != nil {
		return "", err
	}
	payload := strconv.FormatInt(c.now().Add(c.ttl).Unix(), 10) + "." + base64.RawURLEncoding.EncodeToString(nonce)
	mac := hmac.New(sha256.New, c.csrfKey[:])
	_, _ = mac.Write([]byte(email + "\x00" + payload))
	return payload + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func (c *flashCodec) validateCSRF(value, email string) error {
	parts := strings.Split(value, ".")
	if len(parts) != 3 {
		return errors.New("invalid CSRF token")
	}
	exp, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || c.now().Unix() >= exp {
		return errors.New("expired CSRF token")
	}
	payload := parts[0] + "." + parts[1]
	want := hmac.New(sha256.New, c.csrfKey[:])
	_, _ = want.Write([]byte(email + "\x00" + payload))
	got, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || !hmac.Equal(got, want.Sum(nil)) {
		return fmt.Errorf("invalid CSRF token")
	}
	return nil
}
