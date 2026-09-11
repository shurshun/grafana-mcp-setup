package server

import (
	"html/template"
	"strings"
	"unicode"
)

// Colouring the snippet is done here rather than in the browser: the text is
// generated a few lines away, so a scanner over a known shape beats shipping a
// highlighting library to every reader.
//
// The token's place in the text is a sentinel, and it comes out as a span the
// page can mask and the copy handler can fill in.

type painter struct {
	b    strings.Builder
	mask string
}

func (p *painter) span(class, text string) {
	p.b.WriteString(`<span class="` + class + `">`)
	template.HTMLEscape(&p.b, []byte(text))
	p.b.WriteString(`</span>`)
}

func (p *painter) plain(text string) {
	template.HTMLEscape(&p.b, []byte(text))
}

// str writes a quoted string, or the token placeholder when that is what it
// holds.
func (p *painter) str(class, quoted string) {
	if strings.Contains(quoted, tokenSentinel) {
		p.span(class, `"`)
		p.b.WriteString(`<span class="tok">`)
		template.HTMLEscape(&p.b, []byte(p.mask))
		p.b.WriteString(`</span>`)
		p.span(class, `"`)
		return
	}
	p.span(class, quoted)
}

func highlightJSON(src, mask string) template.HTML {
	p := painter{mask: mask}
	runes := []rune(src)

	for i := 0; i < len(runes); i++ {
		switch c := runes[i]; {
		case c == '"':
			j := i + 1
			for j < len(runes) && runes[j] != '"' {
				if runes[j] == '\\' {
					j++
				}
				j++
			}
			if j >= len(runes) {
				j = len(runes) - 1
			}
			quoted := string(runes[i : j+1])

			// A string followed by a colon is a key; everything else is a value.
			class := "s"
			for k := j + 1; k < len(runes); k++ {
				if unicode.IsSpace(runes[k]) {
					continue
				}
				if runes[k] == ':' {
					class = "k"
				}
				break
			}
			p.str(class, quoted)
			i = j

		case unicode.IsDigit(c):
			j := i
			for j < len(runes) && (unicode.IsDigit(runes[j]) || runes[j] == '.') {
				j++
			}
			p.span("n", string(runes[i:j]))
			i = j - 1

		default:
			p.plain(string(c))
		}
	}
	// Every piece went through HTMLEscape on the way in; the tags are this
	// file's own.
	return template.HTML(p.b.String()) // #nosec G203
}

func highlightTOML(src, mask string) template.HTML {
	p := painter{mask: mask}

	for i, line := range strings.Split(src, "\n") {
		if i > 0 {
			p.plain("\n")
		}
		trimmed := strings.TrimSpace(line)

		switch {
		case strings.HasPrefix(trimmed, "["):
			p.span("t", line)

		case strings.Contains(line, "="):
			key, value, _ := strings.Cut(line, "=")
			p.span("k", key)
			p.plain("=")
			p.plainOrStrings(value)

		default:
			p.plain(line)
		}
	}
	return template.HTML(p.b.String()) // #nosec G203
}

// plainOrStrings paints the quoted parts of a TOML value and leaves the rest,
// which covers both a bare string and an array of them.
func (p *painter) plainOrStrings(value string) {
	for {
		start := strings.Index(value, `"`)
		if start < 0 {
			p.plain(value)
			return
		}
		end := strings.Index(value[start+1:], `"`)
		if end < 0 {
			p.plain(value)
			return
		}
		end += start + 2

		p.plain(value[:start])
		p.str("s", value[start:end])
		value = value[end:]
	}
}
