package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"html"
	"net/http"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

type oidcClaims struct {
	Email  string   `json:"email"`
	Groups []string `json:"groups"`
	Nonce  string   `json:"nonce"`
}

func randomURLValue(size int) (string, error) {
	b := make([]byte, size)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func (s *Server) nativeIdentity(r *http.Request) (string, error) {
	if s.session == nil {
		s.recordAuth("oidc_failure")
		return "", &authError{err: errors.New("native authentication is unavailable")}
	}
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		s.recordAuth("oidc_failure")
		return "", &authError{err: errors.New("no native session")}
	}
	payload, err := s.session.openSession(cookie.Value)
	if err != nil {
		s.recordAuth("oidc_failure")
		return "", &authError{err: errors.New("native session is invalid or expired")}
	}
	return s.identityFromIDToken(r.Context(), payload.IDToken, "")
}

func (s *Server) writeIdentityError(w http.ResponseWriter, r *http.Request, err error) {
	if s.cfg.AuthMode == "native" && r.Method == http.MethodGet && !isForbiddenAuthError(err) {
		http.Redirect(w, r, s.cfg.BasePath+"/oauth2/login", http.StatusFound)
		return
	}
	writeAuthError(w, err)
}

func isForbiddenAuthError(err error) bool {
	var ae *authError
	return errors.As(err, &ae) && ae.forbidden
}

func (s *Server) handleOAuthLogin(w http.ResponseWriter, r *http.Request) {
	if s.oauth == nil || s.session == nil {
		http.Error(w, "native authentication is unavailable", http.StatusInternalServerError)
		return
	}
	state, err := randomURLValue(32)
	if err != nil {
		http.Error(w, "could not start authentication", http.StatusInternalServerError)
		return
	}
	nonce, err := randomURLValue(32)
	if err != nil {
		http.Error(w, "could not start authentication", http.StatusInternalServerError)
		return
	}
	codeVerifier, err := randomURLValue(32)
	if err != nil {
		http.Error(w, "could not start authentication", http.StatusInternalServerError)
		return
	}
	stateCookie, err := s.session.sealState(state, nonce, codeVerifier)
	if err != nil {
		http.Error(w, "could not start authentication", http.StatusInternalServerError)
		return
	}
	s.setOAuthStateCookie(w, stateCookie)
	digest := sha256.Sum256([]byte(codeVerifier))
	challenge := base64.RawURLEncoding.EncodeToString(digest[:])
	location := s.oauth.AuthCodeURL(
		state,
		oidc.Nonce(nonce),
		oauth2.SetAuthURLParam("code_challenge", challenge),
		oauth2.SetAuthURLParam("code_challenge_method", "S256"),
	)
	http.Redirect(w, r, location, http.StatusFound)
}

func (s *Server) handleOAuthCallback(w http.ResponseWriter, r *http.Request) {
	s.clearOAuthStateCookie(w)
	if s.oauth == nil || s.session == nil {
		http.Error(w, "native authentication is unavailable", http.StatusInternalServerError)
		return
	}
	if providerError := r.URL.Query().Get("error"); providerError != "" {
		http.Error(w, "authentication failed", http.StatusBadRequest)
		return
	}
	state := r.URL.Query().Get("state")
	code := r.URL.Query().Get("code")
	if state == "" || code == "" {
		http.Error(w, "authentication failed", http.StatusBadRequest)
		return
	}
	cookie, err := r.Cookie(stateCookieName)
	if err != nil {
		http.Error(w, "authentication failed", http.StatusBadRequest)
		return
	}
	statePayload, err := s.session.openState(cookie.Value)
	if err != nil || subtle.ConstantTimeCompare([]byte(state), []byte(statePayload.State)) != 1 {
		http.Error(w, "authentication failed", http.StatusBadRequest)
		return
	}
	exchangeContext := oidc.ClientContext(r.Context(), s.oidcHTTP)
	token, err := s.oauth.Exchange(exchangeContext, code, oauth2.SetAuthURLParam("code_verifier", statePayload.CodeVerifier))
	if err != nil {
		http.Error(w, "authentication failed", http.StatusBadRequest)
		return
	}
	rawIDToken := token.Extra("id_token")
	var idToken string
	switch value := rawIDToken.(type) {
	case string:
		idToken = value
	case []byte:
		idToken = string(value)
	}
	if idToken == "" {
		http.Error(w, "authentication failed", http.StatusBadRequest)
		return
	}
	verified, claims, err := s.verifyIDToken(r.Context(), idToken, statePayload.Nonce)
	if err != nil {
		writeCallbackAuthError(w, err)
		return
	}
	if _, err := s.authorise(claims.Email, claims.Groups); err != nil {
		writeCallbackAuthError(w, err)
		return
	}
	expires := time.Now().Add(s.cfg.SessionTTL)
	if verified.Expiry.Before(expires) {
		expires = verified.Expiry
	}
	sessionCookie, err := s.session.sealSession(idToken, expires)
	if err != nil {
		http.Error(w, "authentication response is too large", http.StatusBadRequest)
		return
	}
	s.setSessionCookie(w, sessionCookie, expires)
	http.Redirect(w, r, s.cfg.BasePath, http.StatusSeeOther)
}

func (s *Server) verifyIDToken(ctx context.Context, raw, expectedNonce string) (*oidc.IDToken, oidcClaims, error) {
	if s.verifier == nil {
		s.recordAuth("oidc_failure")
		return nil, oidcClaims{}, &authError{err: errors.New("OIDC verifier is unavailable")}
	}
	tok, err := s.verifier.Verify(oidc.ClientContext(ctx, s.oidcHTTP), raw)
	if err != nil {
		s.recordAuth("oidc_failure")
		return nil, oidcClaims{}, &authError{err: errors.New("ID token verification failed")}
	}
	var claims oidcClaims
	if err := tok.Claims(&claims); err != nil {
		s.recordAuth("oidc_failure")
		return nil, oidcClaims{}, &authError{err: errors.New("ID token claims are invalid")}
	}
	if expectedNonce != "" && claims.Nonce != expectedNonce {
		s.recordAuth("oidc_failure")
		return nil, oidcClaims{}, &authError{err: errors.New("ID token nonce mismatch")}
	}
	return tok, claims, nil
}

func writeCallbackAuthError(w http.ResponseWriter, err error) {
	var ae *authError
	if errors.As(err, &ae) && ae.forbidden {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	http.Error(w, "authentication failed", http.StatusBadRequest)
}

func (s *Server) handleOAuthLogout(w http.ResponseWriter, r *http.Request) {
	email, err := s.nativeIdentity(r)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	if err := s.validMutationRequest(r, email); err != nil {
		http.Error(w, "request verification failed", http.StatusForbidden)
		return
	}
	s.clearSessionCookie(w)
	s.clearOAuthStateCookie(w)
	s.clearFlashCookie(w)
	http.Redirect(w, r, s.cfg.BasePath+"/oauth2/logged-out", http.StatusSeeOther)
}

func (s *Server) handleOAuthLoggedOut(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	basePath := html.EscapeString(s.cfg.BasePath)
	_, _ = w.Write([]byte("<!doctype html><meta charset=\"utf-8\"><title>Signed out</title><h1>Signed out</h1><p>Your local Grafana MCP session ended.</p><p><a href=\"" + basePath + "/oauth2/login\">Sign in</a></p>"))
}

func (s *Server) setOAuthStateCookie(w http.ResponseWriter, value string) {
	http.SetCookie(w, &http.Cookie{
		Name:     stateCookieName,
		Value:    value,
		Path:     s.cfg.BasePath,
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(oauthStateTTL.Seconds()),
		Expires:  time.Now().Add(oauthStateTTL),
	})
}

func (s *Server) clearOAuthStateCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     stateCookieName,
		Path:     s.cfg.BasePath,
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
		Expires:  time.Unix(1, 0),
	})
}

func (s *Server) setSessionCookie(w http.ResponseWriter, value string, expires time.Time) {
	maxAge := int(time.Until(expires).Seconds())
	if maxAge < 1 {
		maxAge = 1
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    value,
		Path:     s.cfg.BasePath,
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   maxAge,
		Expires:  expires,
	})
}

func (s *Server) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Path:     s.cfg.BasePath,
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
		Expires:  time.Unix(1, 0),
	})
}
