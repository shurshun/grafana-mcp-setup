package server

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"net/http"
	"path"
	"strings"
)

//go:embed assets/app.css assets/app.js
var assetFS embed.FS

// asset is one file served beside the page. The name carries a hash of the
// content, so the response can be cached forever and a release still reaches
// every reader immediately.
type asset struct {
	name        string
	contentType string
	body        []byte
}

var assets = loadAssets()

func loadAssets() map[string]asset {
	out := map[string]asset{}
	for _, file := range []struct{ src, kind string }{
		{"assets/app.css", "text/css; charset=utf-8"},
		{"assets/app.js", "text/javascript; charset=utf-8"},
	} {
		body, err := assetFS.ReadFile(file.src)
		if err != nil {
			panic(fmt.Sprintf("embedded asset %s: %v", file.src, err))
		}

		sum := sha256.Sum256(body)
		base := strings.TrimSuffix(path.Base(file.src), path.Ext(file.src))
		name := fmt.Sprintf("%s.%s%s", base, hex.EncodeToString(sum[:])[:12], path.Ext(file.src))

		out[name] = asset{name: name, contentType: file.kind, body: body}
	}
	return out
}

// assetName returns the hashed file name for a source name such as "app.css".
func assetName(source string) string {
	prefix := strings.TrimSuffix(source, path.Ext(source)) + "."
	for name := range assets {
		if strings.HasPrefix(name, prefix) && path.Ext(name) == path.Ext(source) {
			return name
		}
	}
	return source
}

func (s *Server) assetURL(source string) string {
	return s.cfg.BasePath + "/assets/" + assetName(source)
}

// handleAsset serves the stylesheet and the script. They carry no secret, but
// they sit under the authenticated prefix like everything else here.
func (s *Server) handleAsset(w http.ResponseWriter, r *http.Request) {
	a, ok := assets[r.PathValue("name")]
	if !ok {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", a.contentType)
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(a.body)
}
