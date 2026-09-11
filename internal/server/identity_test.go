package server

import "testing"

// The groups are the whole of the authorisation whenever any is set, so the
// cases that must fail matter more than the ones that pass. Holding any one of
// them is enough: an application's roles are usually separate groups.
func TestAuthorise(t *testing.T) {
	required := []string{"/grafana-mcp", "/grafana-admin"}

	cases := []struct {
		name     string
		required []string
		email    string
		groups   []string
		want     string
	}{
		{"member", required, "someone@example.com", []string{"/grafana-mcp"}, "someone@example.com"},
		{"member of the other group", required, "someone@example.com", []string{"/grafana-admin"}, "someone@example.com"},
		{"member among others", required, "someone@example.com", []string{"/staff", "/grafana-mcp"}, "someone@example.com"},
		{"address is folded", required, "Someone@Example.com", []string{"/grafana-mcp"}, "someone@example.com"},
		{"no group required", nil, "someone@example.com", nil, "someone@example.com"},
		{"authenticated but not a member", required, "someone@example.com", []string{"/staff"}, ""},
		{"no groups claim at all", required, "someone@example.com", nil, ""},
		{"lookalike group", required, "someone@example.com", []string{"/grafana-mcp-readonly"}, ""},
		{"same name without the prefix", required, "someone@example.com", []string{"grafana-mcp"}, ""},
		{"no email", required, "", []string{"/grafana-mcp"}, ""},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := &Server{cfg: Config{RequiredGroups: c.required}}
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
