package server

import (
	"net/http/httptest"
	"os"
	"testing"
)

// Renders each page to a file so the README screenshots can be retaken without
// a running service. Skipped unless DUMP_DIR is set.
func TestDumpPages(t *testing.T) {
	dir := os.Getenv("DUMP_DIR")
	if dir == "" {
		t.Skip("no DUMP_DIR")
	}
	s := &Server{cfg: Config{PublicURL: "https://grafana.example.com", BasePath: "/setup-mcp"}}

	cases := map[string]pageData{
		"landing": {State: stateNone, Email: "engineer@example.com", TTLDays: 90},
		"issued": {State: stateIssued, Email: "engineer@example.com",
			Token: "glsa_XmQ2vK9pLt4RbW7nYc1Fd_8a3e5c90", TTLDays: 90},
		"active": {State: stateActive, Email: "engineer@example.com", Created: "1 Sep 2026",
			Expires: "30 Nov 2026", ExpiresIn: "79", LastUsed: "11 Sep 2026", TTLDays: 90},
	}
	for name, d := range cases {
		w := httptest.NewRecorder()
		s.render(w, d)
		// The directory comes from whoever runs the test, and the names are
		// literals above.
		if err := os.WriteFile(dir+"/"+name+".html", w.Body.Bytes(), 0o600); err != nil { // #nosec G703
			t.Fatal(err)
		}
	}
}
