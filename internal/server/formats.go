package server

import (
	"fmt"
	"html/template"
)

// tokenSentinel stands in for the secret while a snippet is being built, so the
// highlighter treats it as an ordinary string and the span that carries the
// mask can be dropped in afterwards. Printable, because %q would escape a
// control character and the scanner would no longer recognise it.
const tokenSentinel = "__GRAFANA_MCP_TOKEN__"

// mcpFormat is one client's way of spelling the same server: a file, a shape
// and the syntax it is written in.
type mcpFormat struct {
	ID     string
	Name   string
	File   string
	Note   template.HTML
	Accent string
	Icon   template.HTML
	Body   template.HTML
}

// formats renders the snippet for every client. Three JSON shapes and one TOML:
// most clients read mcpServers, VS Code calls it servers, Zed wraps the command
// in an object of its own, and Codex keeps a TOML file.
func formats(publicURL, mask string) []mcpFormat {
	out := []mcpFormat{
		{
			ID:     "claude-code",
			Name:   "Claude Code",
			File:   ".mcp.json",
			Note:   "Project scope. <code>~/.claude.json</code> does the same for every project.",
			Accent: "#d97757",
			Icon:   iconSpark,
			Body:   highlightJSON(mcpServersJSON("mcpServers", publicURL, false), mask),
		},
		{
			ID:     "claude-desktop",
			Name:   "Claude Desktop",
			File:   "claude_desktop_config.json",
			Note:   "Settings → Developer → Edit Config.",
			Accent: "#d97757",
			Icon:   iconWindow,
			Body:   highlightJSON(mcpServersJSON("mcpServers", publicURL, false), mask),
		},
		{
			ID:     "codex",
			Name:   "Codex",
			File:   "~/.codex/config.toml",
			Accent: "#10a37f",
			Icon:   iconTerminal,
			Body:   highlightTOML(codexTOML(publicURL), mask),
		},
		{
			ID:     "cursor",
			Name:   "Cursor",
			File:   ".cursor/mcp.json",
			Note:   "<code>~/.cursor/mcp.json</code> for every project instead.",
			Accent: "#6e7681",
			Icon:   iconCursor,
			Body:   highlightJSON(mcpServersJSON("mcpServers", publicURL, false), mask),
		},
		{
			ID:     "vscode",
			Name:   "VS Code",
			File:   ".vscode/mcp.json",
			Note:   "Copilot reads <code>servers</code>, not <code>mcpServers</code>.",
			Accent: "#3b82f6",
			Icon:   iconBrackets,
			Body:   highlightJSON(mcpServersJSON("servers", publicURL, true), mask),
		},
		{
			ID:     "zed",
			Name:   "Zed",
			File:   "settings.json",
			Note:   "Zed nests the command in an object of its own.",
			Accent: "#8b5cf6",
			Icon:   iconZ,
			Body:   highlightJSON(zedJSON(publicURL), mask),
		},
	}
	return out
}

func mcpServersJSON(key, publicURL string, withType bool) string {
	typeLine := ""
	if withType {
		typeLine = "      \"type\": \"stdio\",\n"
	}
	return fmt.Sprintf(`{
  %q: {
    "grafana": {
%s      "command": "mcp-grafana",
      "args": ["-t", "stdio", "--disable-write"],
      "env": {
        "GRAFANA_URL": %q,
        "GRAFANA_SERVICE_ACCOUNT_TOKEN": %q
      }
    }
  }
}`, key, typeLine, publicURL, tokenSentinel)
}

func zedJSON(publicURL string) string {
	return fmt.Sprintf(`{
  "context_servers": {
    "grafana": {
      "source": "custom",
      "command": {
        "path": "mcp-grafana",
        "args": ["-t", "stdio", "--disable-write"],
        "env": {
          "GRAFANA_URL": %q,
          "GRAFANA_SERVICE_ACCOUNT_TOKEN": %q
        }
      }
    }
  }
}`, publicURL, tokenSentinel)
}

func codexTOML(publicURL string) string {
	return fmt.Sprintf(`[mcp_servers.grafana]
command = "mcp-grafana"
args = ["-t", "stdio", "--disable-write"]

[mcp_servers.grafana.env]
GRAFANA_URL = %q
GRAFANA_SERVICE_ACCOUNT_TOKEN = %q`, publicURL, tokenSentinel)
}
