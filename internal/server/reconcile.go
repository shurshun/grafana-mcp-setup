package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// CleanupOptions selects explicit token cleanup actions.
type CleanupOptions struct {
	Apply         bool
	DeleteExpired bool
	KeepToken     string
}

func cleanupClient(cfg Config) (*grafana, mutationLocker, error) {
	locker, err := newMutationLocker(cfg)
	if err != nil {
		return nil, nil, err
	}
	return &grafana{base: cfg.GrafanaURL, token: cfg.GrafanaToken, mode: cfg.GrafanaAPIMode, namespace: cfg.GrafanaNamespace, hc: &http.Client{Timeout: 15 * time.Second}}, locker, nil
}

func normalizeTargets(emails []string) ([]string, error) {
	if len(emails) == 0 {
		return nil, errors.New("at least one --email target is required")
	}
	out := make([]string, 0, len(emails))
	seen := make(map[string]bool)
	for _, raw := range emails {
		email := strings.ToLower(strings.TrimSpace(raw))
		if email == "" || !strings.Contains(email, "@") {
			return nil, fmt.Errorf("invalid email target %q", raw)
		}
		if !seen[email] {
			seen[email] = true
			out = append(out, email)
		}
	}
	return out, nil
}

// Reconcile reports explicit identities and applies only an explicit cleanup strategy.
func Reconcile(ctx context.Context, cfg Config, emails []string, opts CleanupOptions, out io.Writer) error {
	targets, err := normalizeTargets(emails)
	if err != nil {
		return err
	}
	if opts.Apply && !opts.DeleteExpired && opts.KeepToken == "" {
		return errors.New("reconcile --apply requires --delete-expired or --keep-token")
	}
	if opts.KeepToken != "" && len(targets) != 1 {
		return errors.New("--keep-token requires exactly one --email target")
	}
	g, locker, err := cleanupClient(cfg)
	if err != nil {
		return err
	}
	for _, email := range targets {
		guard, err := locker.Acquire(ctx, email)
		if err != nil {
			return err
		}
		err = reconcileOne(ctx, g, guard, email, opts, out)
		guard.Release()
		if err != nil {
			return err
		}
	}
	return nil
}

func reconcileOne(ctx context.Context, g *grafana, guard mutationGuard, email string, opts CleanupOptions, out io.Writer) error {
	sa, err := g.findServiceAccount(ctx, "mcp-"+email)
	if err != nil {
		return err
	}
	if sa == nil {
		_, _ = fmt.Fprintf(out, "%s account=missing action=none dry_run=%t\n", email, !opts.Apply)
		return nil
	}
	if err := validateServiceAccount(sa); err != nil {
		return fmt.Errorf("%s: %w", email, err)
	}
	tokens, err := g.tokens(ctx, sa.Metadata.Name)
	if err != nil {
		return err
	}
	now := time.Now()
	active, expiredCount, candidates := 0, 0, make([]saToken, 0)
	keepFound := opts.KeepToken == ""
	for _, token := range tokens {
		expired := token.Revoked || token.HasExpired || token.Expiration != nil && !token.Expiration.After(now)
		if expired {
			expiredCount++
		}
		if token.Title == opts.KeepToken && !expired {
			keepFound = true
		}
		if opts.KeepToken != "" && token.Title != opts.KeepToken || opts.DeleteExpired && expired {
			candidates = append(candidates, token)
		}
		if !expired {
			active++
		}
	}
	if !keepFound {
		return fmt.Errorf("keep token %q does not exist or is not live", opts.KeepToken)
	}
	_, _ = fmt.Fprintf(out, "%s account=%s role=%s disabled=%t tokens=%d active=%d expired=%d planned_deletes=%d dry_run=%t\n", email, sa.Metadata.Name, sa.Spec.Role, sa.Spec.Disabled, len(tokens), active, expiredCount, len(candidates), !opts.Apply)
	for _, token := range candidates {
		if !opts.Apply {
			_, _ = fmt.Fprintf(out, "%s token=%s action=delete_token dry_run=true\n", email, token.Title)
			continue
		}
		if err := guard.Check(); err != nil {
			return err
		}
		if err := g.deleteToken(ctx, sa.Metadata.Name, token.ID); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(out, "%s token=%s action=deleted_token dry_run=false\n", email, token.Title)
	}
	return nil
}

// Offboard deletes only exact, Viewer service accounts for explicit identities.
func Offboard(ctx context.Context, cfg Config, emails []string, apply bool, out io.Writer) error {
	targets, err := normalizeTargets(emails)
	if err != nil {
		return err
	}
	g, locker, err := cleanupClient(cfg)
	if err != nil {
		return err
	}
	for _, email := range targets {
		guard, err := locker.Acquire(ctx, email)
		if err != nil {
			return err
		}
		sa, findErr := g.findServiceAccount(ctx, "mcp-"+email)
		if findErr != nil {
			guard.Release()
			return findErr
		}
		if sa == nil {
			_, _ = fmt.Fprintf(out, "%s account=missing action=none dry_run=%t\n", email, !apply)
			guard.Release()
			continue
		}
		if sa.Spec.Role != "Viewer" {
			guard.Release()
			return fmt.Errorf("%s: refusing to delete service account with role %q", email, sa.Spec.Role)
		}
		if !apply {
			_, _ = fmt.Fprintf(out, "%s account=%s action=delete_service_account dry_run=true\n", email, sa.Metadata.Name)
			guard.Release()
			continue
		}
		if err := guard.Check(); err != nil {
			guard.Release()
			return err
		}
		err = g.deleteServiceAccount(ctx, sa.Metadata.Name)
		guard.Release()
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(out, "%s account=%s action=deleted_service_account dry_run=false\n", email, sa.Metadata.Name)
	}
	return nil
}
