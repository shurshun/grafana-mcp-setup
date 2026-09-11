package server

import "testing"

// The group is the whole of the authorisation whenever it is set, so the cases
// that must fail matter more than the one that passes.
func TestAuthorise(t *testing.T) {
	cases := []struct {
		name   string
		group  string
		email  string
		groups []string
		want   string
	}{
		{"member", "/grafana-mcp", "someone@example.com", []string{"/grafana-mcp"}, "someone@example.com"},
		{"member among others", "/grafana-mcp", "someone@example.com", []string{"/staff", "/grafana-mcp"}, "someone@example.com"},
		{"address is folded", "/grafana-mcp", "Someone@Example.com", []string{"/grafana-mcp"}, "someone@example.com"},
		{"no group required", "", "someone@example.com", nil, "someone@example.com"},
		{"authenticated but not a member", "/grafana-mcp", "someone@example.com", []string{"/staff"}, ""},
		{"no groups claim at all", "/grafana-mcp", "someone@example.com", nil, ""},
		{"lookalike group", "/grafana-mcp", "someone@example.com", []string{"/grafana-mcp-readonly"}, ""},
		{"same name without the prefix", "/grafana-mcp", "someone@example.com", []string{"grafana-mcp"}, ""},
		{"no email", "/grafana-mcp", "", []string{"/grafana-mcp"}, ""},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := &Server{cfg: Config{RequiredGroup: c.group}}
			got, err := s.authorise(c.email, c.groups)

			if c.want == "" {
				if err == nil {
					t.Fatalf("authorise(%q, %v) = %q, want a refusal", c.email, c.groups, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("authorise(%q, %v): %v", c.email, c.groups, err)
			}
			if got != c.want {
				t.Errorf("authorise = %q, want %q", got, c.want)
			}
		})
	}
}
