package server

import (
	"strings"
	"testing"
)

// The snippets are built here and painted here, so the test that matters is
// that painting does not mangle them: what the reader copies is pre.textContent
// with the tags stripped, which must equal the source.
func TestHighlightingKeepsTheText(t *testing.T) {
	cases := map[string]string{
		"json": mcpServersJSON("mcpServers", "https://grafana.example.com", false),
		"toml": codexTOML("https://grafana.example.com"),
	}

	for lang, src := range cases {
		t.Run(lang, func(t *testing.T) {
			var painted string
			if lang == "json" {
				painted = string(highlightJSON(src, "****"))
			} else {
				painted = string(highlightTOML(src, "****"))
			}

			got := unescape(stripTags(painted))
			want := strings.ReplaceAll(src, tokenSentinel, "****")
			if got != want {
				t.Errorf("painting changed the text:\n got %q\nwant %q", got, want)
			}
		})
	}
}

func TestHighlightingMarksKeysAndStrings(t *testing.T) {
	painted := string(highlightJSON(mcpServersJSON("mcpServers", "https://grafana.example.com", false), "****"))

	for _, want := range []string{
		`<span class="k">&#34;mcpServers&#34;</span>`,
		`<span class="s">&#34;https://grafana.example.com&#34;</span>`,
		`<span class="tok">****</span>`,
	} {
		if !strings.Contains(painted, want) {
			t.Errorf("painted JSON is missing %s", want)
		}
	}

	toml := string(highlightTOML(codexTOML("https://grafana.example.com"), "****"))
	if !strings.Contains(toml, `<span class="t">[mcp_servers.grafana]</span>`) {
		t.Error("painted TOML does not mark the table header")
	}
}

func stripTags(s string) string {
	var b strings.Builder
	depth := 0
	for _, r := range s {
		switch {
		case r == '<':
			depth++
		case r == '>':
			depth--
		case depth == 0:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func unescape(s string) string {
	r := strings.NewReplacer("&#34;", `"`, "&amp;", "&", "&lt;", "<", "&gt;", ">", "&#39;", "'")
	return r.Replace(s)
}
