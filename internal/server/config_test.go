package server

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestSplitList(t *testing.T) {
	cases := map[string][]string{
		"":                       nil,
		"  ":                     nil,
		"one":                    {"one"},
		"one,two":                {"one", "two"},
		" one , two ,, three , ": {"one", "two", "three"},
	}
	for in, want := range cases {
		if got := splitList(in); !slices.Equal(got, want) {
			t.Errorf("splitList(%q) = %v, want %v", in, got, want)
		}
	}
}

func validEnv(t *testing.T) {
	t.Helper()
	t.Setenv("APP_PUBLIC_URL", "https://grafana.example.com")
	t.Setenv("GRAFANA_URL", "http://grafana.monitoring.svc")
	t.Setenv("GRAFANA_API_MODE", "")
	t.Setenv("GRAFANA_PUBLIC_URL", "https://grafana.example.com")
	t.Setenv("GRAFANA_ADMIN_TOKEN", "admin")
	t.Setenv("GRAFANA_ADMIN_TOKEN_FILE", "")
	t.Setenv("FLASH_COOKIE_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	t.Setenv("FLASH_COOKIE_KEY_FILE", "")
	t.Setenv("OIDC_ISSUER", "https://id.example.com")
	t.Setenv("REQUIRED_GROUPS", "/grafana-mcp")
	t.Setenv("ALLOW_ALL_AUTHENTICATED_USERS", "false")
	t.Setenv("ID_TOKEN_SOURCE", "header")
	t.Setenv("ROTATION_LOCK_MODE", "local")
}

func TestFromEnvDefaultsToLegacyGrafanaAPIAndRejectsUnknownMode(t *testing.T) {
	validEnv(t)
	cfg, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GrafanaAPIMode != grafanaAPIModeLegacy {
		t.Fatalf("Grafana API mode = %q; want %q", cfg.GrafanaAPIMode, grafanaAPIModeLegacy)
	}
	t.Setenv("GRAFANA_API_MODE", "auto")
	if _, err := FromEnv(); err == nil || !strings.Contains(err.Error(), "legacy or iam") {
		t.Fatalf("unknown Grafana API mode error = %v", err)
	}
}

func TestFromEnvFailsClosedAndSeparatesOrigins(t *testing.T) {
	validEnv(t)
	t.Setenv("REQUIRED_GROUPS", "")
	if _, err := FromEnv(); err == nil || !strings.Contains(err.Error(), "ALLOW_ALL_AUTHENTICATED_USERS") {
		t.Fatalf("empty groups error = %v", err)
	}
	t.Setenv("ALLOW_ALL_AUTHENTICATED_USERS", "true")
	cfg, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AppOrigin != "https://grafana.example.com" || cfg.IDTokenHeader != "X-Grafana-MCP-ID-Token" {
		t.Fatalf("config = %#v", cfg)
	}
}

func TestFromEnvReadsFileCredentialsAndRejectsAmbiguity(t *testing.T) {
	validEnv(t)
	dir := t.TempDir()
	adminFile := filepath.Join(dir, "admin")
	flashFile := filepath.Join(dir, "flash")
	if err := os.WriteFile(adminFile, []byte("from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(flashFile, []byte(base64.StdEncoding.EncodeToString(make([]byte, 32))), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GRAFANA_ADMIN_TOKEN", "")
	t.Setenv("GRAFANA_ADMIN_TOKEN_FILE", adminFile)
	t.Setenv("FLASH_COOKIE_KEY", "")
	t.Setenv("FLASH_COOKIE_KEY_FILE", flashFile)
	cfg, err := FromEnv()
	if err != nil || cfg.GrafanaToken != "from-file" {
		t.Fatalf("file credentials = %q, %v", cfg.GrafanaToken, err)
	}
	t.Setenv("GRAFANA_ADMIN_TOKEN", "also-env")
	if _, err := FromEnv(); err == nil || !strings.Contains(err.Error(), "set only one") {
		t.Fatalf("ambiguous credentials error = %v", err)
	}
}

func TestFromEnvRejectsUnsafeHeaderOriginAndTTL(t *testing.T) {
	validEnv(t)
	t.Setenv("ID_TOKEN_HEADER", "Bad Header")
	if _, err := FromEnv(); err == nil || !strings.Contains(err.Error(), "header name") {
		t.Fatalf("bad header error = %v", err)
	}
	t.Setenv("ID_TOKEN_HEADER", "X-Grafana-MCP-ID-Token")
	t.Setenv("APP_PUBLIC_URL", "https://grafana.example.com/setup-mcp")
	if _, err := FromEnv(); err == nil || !strings.Contains(err.Error(), "origin") {
		t.Fatalf("app URL path error = %v", err)
	}
	t.Setenv("APP_PUBLIC_URL", "https://grafana.example.com")
	t.Setenv("TOKEN_TTL_DAYS", "999999999999999999999")
	if _, err := FromEnv(); err == nil || !strings.Contains(err.Error(), "1 to 365") {
		t.Fatalf("TTL overflow error = %v", err)
	}
}

func TestOriginCanonicalization(t *testing.T) {
	for raw, want := range map[string]string{
		"https://APP.Example:443/": "https://app.example",
		"http://APP.Example:80":    "http://app.example",
		"https://APP.Example:8443": "https://app.example:8443",
		"https://[::1]:443":        "https://[::1]",
	} {
		got, err := normalizeOrigin(raw)
		if err != nil || got != want {
			t.Fatalf("normalizeOrigin(%q) = %q, %v; want %q", raw, got, err, want)
		}
	}
	for _, raw := range []string{"https://app.example:99999", "https://app.example:0", "null", "https://app.example/path"} {
		if _, err := normalizeOrigin(raw); err == nil {
			t.Fatalf("invalid origin accepted: %q", raw)
		}
	}
}

func TestEnvironmentSetupFlag(t *testing.T) {
	validEnv(t)
	for _, value := range []string{"", "false", "true", "invalid"} {
		t.Setenv("ENABLE_ENV_SETUP", value)
		cfg, err := FromEnv()
		if value == "invalid" {
			if err == nil || !strings.Contains(err.Error(), "ENABLE_ENV_SETUP") {
				t.Fatal("invalid flag accepted")
			}
		} else if err != nil || cfg.EnableEnvSetup != (value == "true") {
			t.Fatalf("flag %q: %v, %v", value, cfg.EnableEnvSetup, err)
		}
	}
}
