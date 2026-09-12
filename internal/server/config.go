package server

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	defaultRotationTokenFile = "/var/run/secrets/rotation-lock/token"
	defaultRotationCAFile    = "/var/run/secrets/rotation-lock/ca.crt"
)

// Config defines issuer, authentication, and runtime settings.
type Config struct {
	Addr string

	BasePath     string
	AppPublicURL string
	AppOrigin    string

	GrafanaURL       string
	GrafanaToken     string
	GrafanaAPIMode   string
	GrafanaNamespace string
	PublicURL        string

	Issuer        string
	ClientID      string
	IDTokenSource string
	IDTokenHeader string
	IDTokenCookie string

	RequiredGroups             []string
	AllowAllAuthenticatedUsers bool

	TokenTTL       time.Duration
	FlashCookieKey []byte
	FlashTTL       time.Duration
	StartupTimeout time.Duration
	ReadinessTTL   time.Duration

	RotationLockMode  string
	RotationLease     string
	PodNamespace      string
	PodName           string
	RotationTokenFile string
	RotationCAFile    string
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func splitList(v string) []string {
	var out []string
	for _, part := range strings.Split(v, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func boolEnv(key string) (bool, error) {
	v := os.Getenv(key)
	if v == "" {
		return false, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("%s must be true or false", key)
	}
	return b, nil
}

func secretEnv(valueKey, fileKey string) (string, error) {
	value, file := os.Getenv(valueKey), os.Getenv(fileKey)
	if value != "" && file != "" {
		return "", fmt.Errorf("set only one of %s and %s", valueKey, fileKey)
	}
	if value != "" {
		return value, nil
	}
	if file == "" {
		return "", fmt.Errorf("set one of %s and %s", valueKey, fileKey)
	}
	b, err := os.ReadFile(file) // #nosec G304 G703 -- the operator supplies this credential path.
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", fileKey, err)
	}
	value = strings.TrimSpace(string(b))
	if value == "" {
		return "", fmt.Errorf("%s is empty", fileKey)
	}
	return value, nil
}

func validHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for _, c := range []byte(name) {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' {
			continue
		}
		switch c {
		case '!', '#', '$', '%', '&', '\'', '*', '+', '-', '.', '^', '_', '`', '|', '~':
			continue
		}
		return false
	}
	return true
}

// FromEnv validates runtime settings and loads configured credentials.
func FromEnv() (Config, error) {
	allowAll, err := boolEnv("ALLOW_ALL_AUTHENTICATED_USERS")
	if err != nil {
		return Config{}, err
	}

	c := Config{
		Addr:                       env("LISTEN_ADDR", ":8080"),
		BasePath:                   "/" + strings.Trim(env("BASE_PATH", "/setup-mcp"), "/"),
		AppPublicURL:               strings.TrimRight(env("APP_PUBLIC_URL", ""), "/"),
		GrafanaURL:                 strings.TrimRight(env("GRAFANA_URL", ""), "/"),
		GrafanaAPIMode:             env("GRAFANA_API_MODE", "legacy"),
		GrafanaNamespace:           env("GRAFANA_NAMESPACE", "default"),
		PublicURL:                  strings.TrimRight(env("GRAFANA_PUBLIC_URL", ""), "/"),
		Issuer:                     strings.TrimRight(env("OIDC_ISSUER", ""), "/"),
		ClientID:                   env("OIDC_CLIENT_ID", "grafana-mcp-setup"),
		IDTokenSource:              env("ID_TOKEN_SOURCE", "header"),
		IDTokenHeader:              env("ID_TOKEN_HEADER", "X-Grafana-MCP-ID-Token"),
		IDTokenCookie:              env("ID_TOKEN_COOKIE", "mcp_id_token"),
		RequiredGroups:             splitList(os.Getenv("REQUIRED_GROUPS")),
		AllowAllAuthenticatedUsers: allowAll,
		FlashTTL:                   5 * time.Minute,
		StartupTimeout:             10 * time.Second,
		ReadinessTTL:               10 * time.Second,
		RotationLockMode:           env("ROTATION_LOCK_MODE", "local"),
		RotationLease:              env("ROTATION_LEASE_NAME", "grafana-mcp-rotation"),
		PodNamespace:               os.Getenv("POD_NAMESPACE"),
		PodName:                    os.Getenv("POD_NAME"),
		RotationTokenFile:          defaultRotationTokenFile,
		RotationCAFile:             defaultRotationCAFile,
	}

	days, err := strconv.Atoi(env("TOKEN_TTL_DAYS", "90"))
	if err != nil || days <= 0 || days > 365 {
		return c, errors.New("TOKEN_TTL_DAYS must be an integer from 1 to 365")
	}
	c.TokenTTL = time.Duration(days) * 24 * time.Hour

	c.GrafanaToken, err = secretEnv("GRAFANA_ADMIN_TOKEN", "GRAFANA_ADMIN_TOKEN_FILE")
	if err != nil {
		return c, err
	}
	flashEncoded, err := secretEnv("FLASH_COOKIE_KEY", "FLASH_COOKIE_KEY_FILE")
	if err != nil {
		return c, err
	}
	c.FlashCookieKey, err = base64.StdEncoding.DecodeString(flashEncoded)
	if err != nil || len(c.FlashCookieKey) != 32 {
		return c, errors.New("FLASH_COOKIE_KEY must be base64 for exactly 32 bytes")
	}

	for name, v := range map[string]string{
		"APP_PUBLIC_URL":     c.AppPublicURL,
		"GRAFANA_URL":        c.GrafanaURL,
		"GRAFANA_PUBLIC_URL": c.PublicURL,
		"OIDC_ISSUER":        c.Issuer,
		"GRAFANA_NAMESPACE":  c.GrafanaNamespace,
	} {
		if v == "" {
			return c, errors.New(name + " is required")
		}
	}
	if len(c.RequiredGroups) == 0 && !c.AllowAllAuthenticatedUsers {
		return c, errors.New("REQUIRED_GROUPS must be nonempty unless ALLOW_ALL_AUTHENTICATED_USERS=true")
	}
	if c.IDTokenSource != "header" && c.IDTokenSource != "cookie" {
		return c, errors.New("ID_TOKEN_SOURCE must be header or cookie")
	}
	if c.IDTokenSource == "header" && !validHeaderName(c.IDTokenHeader) {
		return c, errors.New("ID_TOKEN_HEADER must be a valid HTTP header name in header mode")
	}
	if c.IDTokenSource == "cookie" && c.IDTokenCookie == "" {
		return c, errors.New("ID_TOKEN_COOKIE is required in cookie mode")
	}
	if c.IDTokenSource == "cookie" && !validHeaderName(c.IDTokenCookie) {
		return c, errors.New("ID_TOKEN_COOKIE must be a valid cookie name in cookie mode")
	}
	if c.BasePath == "/" || strings.ContainsAny(c.BasePath, "{}?#% \t\r\n") || strings.Contains(c.BasePath, "//") || strings.Contains(c.BasePath, "/../") || strings.HasSuffix(c.BasePath, "/..") {
		return c, errors.New("BASE_PATH must be a non-root, unescaped URL path")
	}
	if c.RotationLockMode != "local" && c.RotationLockMode != "kubernetes" {
		return c, errors.New("ROTATION_LOCK_MODE must be local or kubernetes")
	}
	if c.GrafanaAPIMode != "legacy" && c.GrafanaAPIMode != "iam" {
		return c, errors.New("GRAFANA_API_MODE must be legacy or iam")
	}
	if c.RotationLockMode == "kubernetes" && (c.RotationLease == "" || c.PodNamespace == "" || c.PodName == "") {
		return c, errors.New("ROTATION_LEASE_NAME, POD_NAMESPACE, and POD_NAME are required in kubernetes lock mode")
	}

	appOrigin, err := normalizeOrigin(c.AppPublicURL)
	if err != nil {
		return c, errors.New("APP_PUBLIC_URL must contain only an absolute http or https origin")
	}
	c.AppOrigin = appOrigin
	c.AppPublicURL = c.AppOrigin
	for name, raw := range map[string]string{"GRAFANA_URL": c.GrafanaURL, "GRAFANA_PUBLIC_URL": c.PublicURL} {
		u, err := url.Parse(raw)
		if err != nil || u.Scheme == "" || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return c, errors.New(name + " must be an absolute http or https URL without credentials, query, or fragment")
		}
	}
	return c, nil
}

func normalizeOrigin(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return "", errors.New("invalid origin")
	}
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if port != "" {
		number, err := strconv.Atoi(port)
		if err != nil || number < 1 || number > 65535 {
			return "", errors.New("invalid origin port")
		}
		port = strconv.Itoa(number)
		if u.Scheme == "https" && port == "443" || u.Scheme == "http" && port == "80" {
			port = ""
		}
	}
	if port != "" {
		host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return u.Scheme + "://" + host, nil
}
