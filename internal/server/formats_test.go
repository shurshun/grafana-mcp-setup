package server

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func TestLaunchModesKeepSecretsOutOfCommandArguments(t *testing.T) {
	for _, mode := range []string{"binary", "uvx", "docker"} {
		t.Run(mode, func(t *testing.T) {
			var config struct {
				Servers map[string]struct {
					Command string            `json:"command"`
					Args    []string          `json:"args"`
					Env     map[string]string `json:"env"`
				} `json:"mcpServers"`
			}
			if err := json.Unmarshal([]byte(mcpServersJSON("mcpServers", "https://grafana.example.com/subpath", false, mode)), &config); err != nil {
				t.Fatal(err)
			}
			s := config.Servers["grafana"]
			if !slices.Contains(s.Args, "--disable-write") || !slices.Contains(s.Args, "stdio") {
				t.Fatal("client lost read-only stdio settings")
			}
			if s.Env["GRAFANA_SERVICE_ACCOUNT_TOKEN"] != tokenSentinel || s.Env["GRAFANA_URL"] != "https://grafana.example.com/subpath" {
				t.Fatal("client lost its environment credentials or URL")
			}
			for _, arg := range s.Args {
				if strings.Contains(arg, tokenSentinel) {
					t.Fatal("token was placed in process arguments")
				}
			}
			if mode == "docker" {
				if s.Command != "docker" || !slices.Contains(s.Args, "-i") || slices.Contains(s.Args, "-t") && slices.Index(s.Args, "-t") < slices.Index(s.Args, "grafana/mcp-grafana:"+mcpVersion) {
					t.Fatal("Docker must preserve stdin without allocating a terminal")
				}
				for _, name := range []string{"GRAFANA_URL", "GRAFANA_SERVICE_ACCOUNT_TOKEN"} {
					i := slices.Index(s.Args, name)
					if i < 1 || s.Args[i-1] != "-e" {
						t.Fatalf("Docker does not forward %s from the environment", name)
					}
				}
			}
			if mode == "uvx" && (s.Command != "uvx" || s.Args[0] != "mcp-grafana=="+mcpVersion) {
				t.Fatal("uvx package must use the pinned version")
			}
		})
	}
}

func TestEnvironmentExamplesOmitToken(t *testing.T) {
	for _, client := range formats("https://grafana.example.com", "SECRET_MASK") {
		for _, variant := range client.Variants {
			body := unescape(stripTags(string(variant.EnvBody)))
			for _, forbidden := range []string{tokenSentinel, "SECRET_MASK", `GRAFANA_SERVICE_ACCOUNT_TOKEN =`, `"GRAFANA_SERVICE_ACCOUNT_TOKEN":`} {
				if strings.Contains(body, forbidden) {
					t.Fatalf("%s/%s env contains %q", client.ID, variant.ID, forbidden)
				}
			}
			for _, want := range []string{"/Users/example/work/project", "--disable-write", "https://grafana.example.com", `"direnv"`} {
				if !strings.Contains(body, want) {
					t.Fatalf("%s/%s env missing %q", client.ID, variant.ID, want)
				}
			}
			if client.ID != "codex" && !json.Valid([]byte(body)) {
				t.Fatalf("%s/%s invalid JSON", client.ID, variant.ID)
			}
		}
	}
}

func TestEveryClientHasThreeMaskedLaunchModes(t *testing.T) {
	clients := formats("https://grafana.example.com", "MASKED")
	if len(clients) != 6 {
		t.Fatal("expected six client formats")
	}
	for _, client := range clients {
		if len(client.Variants) != 3 {
			t.Fatalf("%s does not offer all launch modes", client.ID)
		}
		for _, variant := range client.Variants {
			body := unescape(stripTags(string(variant.Body)))
			if strings.Contains(body, tokenSentinel) || !strings.Contains(body, "MASKED") {
				t.Fatalf("%s/%s has an unmasked placeholder", client.ID, variant.ID)
			}
			if client.ID != "codex" && !json.Valid([]byte(body)) {
				t.Fatalf("%s/%s is invalid JSON", client.ID, variant.ID)
			}
		}
	}
}

func TestJSONFormatsRoundTripHostileURL(t *testing.T) {
	publicURL := "https://grafana.example.test/a\"b\\c\n\t\x01"

	for _, key := range []string{"mcpServers", "servers"} {
		t.Run(key, func(t *testing.T) {
			var config mcpServersRootJSON
			raw := mcpServersJSON(key, publicURL, key == "servers")
			if err := json.Unmarshal([]byte(raw), &config); err != nil {
				t.Fatal(err)
			}
			servers := config.MCPServers
			if key == "servers" {
				servers = config.Servers
			}
			if got := servers["grafana"].Env.URL; got != publicURL {
				t.Fatalf("URL after JSON round trip = %q; want %q", got, publicURL)
			}
		})
	}

	t.Run("zed", func(t *testing.T) {
		var config zedRootJSON
		raw := zedJSON(publicURL)
		if err := json.Unmarshal([]byte(raw), &config); err != nil {
			t.Fatal(err)
		}
		if got := config.ContextServers.Grafana.Command.Env.URL; got != publicURL {
			t.Fatalf("URL after JSON round trip = %q; want %q", got, publicURL)
		}
	})
}

func TestCodexTOMLPreservesLaunchCommandAndEscapesStrings(t *testing.T) {
	publicURL := "https://grafana.example.test/a\"b\\c\n"
	got := codexTOML(publicURL, "docker")
	for _, want := range []string{
		`command = "docker"`,
		`args = ["run", "--rm", "-i", "-e", "GRAFANA_URL", "-e", "GRAFANA_SERVICE_ACCOUNT_TOKEN", "grafana/mcp-grafana:1.4.1", "-t", "stdio", "--disable-write"]`,
		`GRAFANA_URL = "https://grafana.example.test/a\"b\\c\n"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("Codex TOML does not contain %q:\n%s", want, got)
		}
	}
}
