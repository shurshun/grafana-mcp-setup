package server

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is the whole of the configuration. Everything arrives through the
// environment, so a chart or a compose file can set it without a config file.
type Config struct {
	Addr string

	// BasePath is where the service is mounted. The reverse proxy in front of
	// it authenticates this prefix and nothing else, which is how Grafana's own
	// routes stay untouched.
	BasePath string

	// GrafanaURL is reached from inside the network; PublicURL is what goes
	// into the .mcp.json the page hands out.
	GrafanaURL   string
	GrafanaToken string
	PublicURL    string

	// Issuer and ClientID verify the ID token the proxy leaves in a cookie.
	Issuer        string
	ClientID      string
	IDTokenCookie string

	// RequiredGroups, when set, is the whole of the authorisation: the token
	// goes only to people the identity provider puts in one of these groups.
	// Several because an application's roles are often separate groups rather
	// than nested ones, and holding any of them should be enough.
	//
	// Leave it empty only when signing in is already the permission you want to
	// reuse — which it usually is not, since the proxy in front authenticates
	// against the identity provider, not against Grafana.
	RequiredGroups []string

	TokenTTL time.Duration
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// splitList reads a comma-separated list, ignoring blanks and stray spaces.
func splitList(v string) []string {
	var out []string
	for _, part := range strings.Split(v, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// FromEnv reads the configuration and refuses to return a half-built one: a
// missing value is a startup failure, not a surprise at the first request.
func FromEnv() (Config, error) {
	c := Config{
		Addr:           env("LISTEN_ADDR", ":8080"),
		BasePath:       "/" + strings.Trim(env("BASE_PATH", "/setup-mcp"), "/"),
		GrafanaURL:     strings.TrimRight(env("GRAFANA_URL", ""), "/"),
		GrafanaToken:   os.Getenv("GRAFANA_ADMIN_TOKEN"),
		PublicURL:      strings.TrimRight(env("GRAFANA_PUBLIC_URL", ""), "/"),
		Issuer:         strings.TrimRight(env("OIDC_ISSUER", ""), "/"),
		ClientID:       env("OIDC_CLIENT_ID", "grafana-mcp-setup"),
		IDTokenCookie:  env("ID_TOKEN_COOKIE", "mcp_id_token"),
		RequiredGroups: splitList(os.Getenv("REQUIRED_GROUPS")),
	}

	days, err := strconv.Atoi(env("TOKEN_TTL_DAYS", "90"))
	if err != nil || days <= 0 {
		return c, errors.New("TOKEN_TTL_DAYS must be a positive integer")
	}
	c.TokenTTL = time.Duration(days) * 24 * time.Hour

	for name, v := range map[string]string{
		"GRAFANA_URL":         c.GrafanaURL,
		"GRAFANA_ADMIN_TOKEN": c.GrafanaToken,
		"GRAFANA_PUBLIC_URL":  c.PublicURL,
		"OIDC_ISSUER":         c.Issuer,
	} {
		if v == "" {
			return c, errors.New(name + " is required")
		}
	}
	return c, nil
}
