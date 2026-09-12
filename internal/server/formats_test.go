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
