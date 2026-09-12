package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

func TestAuditPartialAndErrorRedaction(t *testing.T) {
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	s := &Server{cfg: Config{Issuer: "https://idp.example.com"}}
	ctx := context.WithValue(context.Background(), requestIDKey{}, "request-one")
	s.audit(ctx, "token_issued_partial_cleanup", "user@example.com", nil, "service_account_id", "sa-one", "token_name", "token-one")
	var record map[string]any
	if err := json.Unmarshal(buf.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	if record["result"] != "partial" || record["issuer"] != s.cfg.Issuer || record["service_account_id"] != "sa-one" || record["token_name"] != "token-one" {
		t.Fatalf("unexpected audit metadata: %v", record)
	}
	buf.Reset()
	s.audit(ctx, "issue_failed", "user@example.com", errors.New("secret-value-from-upstream"))
	if strings.Contains(buf.String(), "secret-value-from-upstream") || strings.Contains(buf.String(), "user@example.com") {
		t.Fatal("audit disclosed raw error or email")
	}
}

func TestAuditReasonsDistinguishSafeFailureClasses(t *testing.T) {
	if auditReason(context.DeadlineExceeded) != "timeout" {
		t.Fatal("deadline has no stable reason")
	}
	if auditReason(&apiError{Status: 403}) != "grafana_forbidden" {
		t.Fatal("Grafana authorization has no stable reason")
	}
	if auditReason(errServiceAccountState) != "invalid_service_account" {
		t.Fatal("role/state drift has no stable reason")
	}
}
