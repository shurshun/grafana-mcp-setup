package server

import (
	"encoding/json"
	"fmt"
	"html/template"
)

// tokenSentinel stands in for the secret while a snippet is being built, so the
// highlighter treats it as an ordinary string and the span that carries the
// mask can be dropped in afterwards. Printable, because %q would escape a
// control character and the scanner would no longer recognise it.
const tokenSentinel = "__GRAFANA_MCP_TOKEN__"

const mcpVersion = "1.4.1"

type mcpVariant struct {
	ID   string
	Name string
	Note string
	Body template.HTML
}

// mcpFormat is one client's way of spelling the same server: a file, a shape
// and the syntax it is written in.
type mcpFormat struct {
	ID       string
	Name     string
	File     string
	Note     template.HTML
	Accent   string
	Icon     template.HTML
	Body     template.HTML
	Variants []mcpVariant
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
	for i := range out {
		for _, mode := range []string{"binary", "uvx", "docker"} {
			v := mcpVariant{ID: mode}
			switch mode {
			case "binary":
				v.Name, v.Note = "Installed binary", "Install mcp-grafana "+mcpVersion+" and make it available in PATH."
			case "uvx":
				v.Name, v.Note = "uvx", "Requires uv. Downloads mcp-grafana "+mcpVersion+"."
			case "docker":
				v.Name, v.Note = "Docker", "Requires Docker. The container must be able to reach the Grafana URL."
			}
			switch out[i].ID {
			case "codex":
				v.Body = highlightTOML(codexTOML(publicURL, mode), mask)
			case "zed":
				v.Body = highlightJSON(zedJSON(publicURL, mode), mask)
			case "vscode":
				v.Body = highlightJSON(mcpServersJSON("servers", publicURL, true, mode), mask)
			default:
				v.Body = highlightJSON(mcpServersJSON("mcpServers", publicURL, false, mode), mask)
			}
			out[i].Variants = append(out[i].Variants, v)
		}
	}
	return out
}

func launchCommand(modes ...string) (string, string) {
	mode := "binary"
	if len(modes) > 0 {
		mode = modes[0]
	}
	command, args := "mcp-grafana", []string{"-t", "stdio", "--disable-write"}
	switch mode {
	case "uvx":
		command = "uvx"
		args = append([]string{"mcp-grafana==" + mcpVersion}, args...)
	case "docker":
		command = "docker"
		args = append([]string{"run", "--rm", "-i", "-e", "GRAFANA_URL", "-e", "GRAFANA_SERVICE_ACCOUNT_TOKEN", "grafana/mcp-grafana:" + mcpVersion}, args...)
	}
	encoded, _ := json.Marshal(args)
	return command, string(encoded)
}

func mcpServersJSON(key, publicURL string, withType bool, mode ...string) string {
	command, args := launchCommand(mode...)
	typeLine := ""
	if withType {
		typeLine = "      \"type\": \"stdio\",\n"
	}
	return fmt.Sprintf(`{
  %q: {
    "grafana": {
%s      "command": %q,
      "args": %s,
      "env": {
        "GRAFANA_URL": %q,
        "GRAFANA_SERVICE_ACCOUNT_TOKEN": %q
      }
    }
  }
}`, key, typeLine, command, args, publicURL, tokenSentinel)
}

func zedJSON(publicURL string, mode ...string) string {
	command, args := launchCommand(mode...)
	return fmt.Sprintf(`{
  "context_servers": {
    "grafana": {
      "source": "custom",
      "command": {
        "path": %q,
        "args": %s,
        "env": {
          "GRAFANA_URL": %q,
          "GRAFANA_SERVICE_ACCOUNT_TOKEN": %q
        }
      }
    }
  }
}`, command, args, publicURL, tokenSentinel)
}

func codexTOML(publicURL string, mode ...string) string {
	command, args := launchCommand(mode...)
	return fmt.Sprintf(`[mcp_servers.grafana]
command = %q
args = %s

[mcp_servers.grafana.env]
GRAFANA_URL = %q
GRAFANA_SERVICE_ACCOUNT_TOKEN = %q`, command, args, publicURL, tokenSentinel)
}
