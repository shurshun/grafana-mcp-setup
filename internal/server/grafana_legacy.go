package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

const legacyServiceAccountsPath = "/api/serviceaccounts"

type legacyServiceAccount struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Role       string `json:"role"`
	IsDisabled bool   `json:"isDisabled"`
}

type legacySearchPage struct {
	TotalCount      int                    `json:"totalCount"`
	ServiceAccounts []legacyServiceAccount `json:"serviceAccounts"`
}

type legacyToken struct {
	ID         int64      `json:"id"`
	Name       string     `json:"name"`
	Created    time.Time  `json:"created"`
	Expiration *time.Time `json:"expiration"`
	LastUsedAt *time.Time `json:"lastUsedAt"`
	HasExpired bool       `json:"hasExpired"`
}

func normalizeLegacyServiceAccount(wire legacyServiceAccount) (*serviceAccount, error) {
	if wire.ID <= 0 {
		return nil, errors.New("grafana returned an invalid legacy service account ID")
	}
	sa := &serviceAccount{}
	sa.Metadata.Name = strconv.FormatInt(wire.ID, 10)
	sa.Spec.Title = wire.Name
	sa.Spec.Role = wire.Role
	sa.Spec.Disabled = wire.IsDisabled
	return sa, nil
}

func legacyNumericID(kind, raw string) (string, error) {
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return "", fmt.Errorf("invalid legacy Grafana %s ID", kind)
	}
	return strconv.FormatInt(id, 10), nil
}

func (g *grafana) legacyReady(ctx context.Context) error {
	q := url.Values{"page": {"1"}, "perpage": {"1"}}
	var page legacySearchPage
	if err := g.do(ctx, http.MethodGet, legacyServiceAccountsPath+"/search?"+q.Encode(), nil, &page); err != nil {
		return fmt.Errorf("grafana legacy service account API is unavailable: %w", err)
	}
	return nil
}

func (g *grafana) legacyFindServiceAccount(ctx context.Context, title string) (*serviceAccount, error) {
	const perPage = 100
	seen := 0
	var found *serviceAccount
	for pageNumber := 1; pageNumber <= 10000; pageNumber++ {
		q := url.Values{
			"page":    {strconv.Itoa(pageNumber)},
			"perpage": {strconv.Itoa(perPage)},
			"query":   {title},
		}
		var page legacySearchPage
		if err := g.do(ctx, http.MethodGet, legacyServiceAccountsPath+"/search?"+q.Encode(), nil, &page); err != nil {
			return nil, err
		}
		if page.TotalCount < 0 {
			return nil, errors.New("grafana returned an invalid legacy service account count")
		}
		for _, wire := range page.ServiceAccounts {
			if wire.Name != title {
				continue
			}
			if found != nil {
				return nil, fmt.Errorf("multiple Grafana service accounts have title %q", title)
			}
			account, err := normalizeLegacyServiceAccount(wire)
			if err != nil {
				return nil, err
			}
			found = account
		}
		seen += len(page.ServiceAccounts)
		if len(page.ServiceAccounts) < perPage || seen >= page.TotalCount {
			return found, nil
		}
	}
	return nil, errors.New("grafana legacy service account pagination exceeded its limit")
}

func (g *grafana) legacyCreateServiceAccount(ctx context.Context, title string) (*serviceAccount, error) {
	body := struct {
		Name       string `json:"name"`
		Role       string `json:"role"`
		IsDisabled bool   `json:"isDisabled"`
	}{Name: title, Role: "Viewer", IsDisabled: false}
	var wire legacyServiceAccount
	if err := g.do(ctx, http.MethodPost, legacyServiceAccountsPath, body, &wire); err != nil {
		return nil, err
	}
	return normalizeLegacyServiceAccount(wire)
}

func (g *grafana) legacyDeleteServiceAccount(ctx context.Context, serviceAccountID string) error {
	id, err := legacyNumericID("service account", serviceAccountID)
	if err != nil {
		return err
	}
	return g.do(ctx, http.MethodDelete, legacyServiceAccountsPath+"/"+id, nil, nil)
}

func (g *grafana) legacyTokens(ctx context.Context, serviceAccountID string) ([]saToken, error) {
	id, err := legacyNumericID("service account", serviceAccountID)
	if err != nil {
		return nil, err
	}
	var wire []legacyToken
	if err := g.do(ctx, http.MethodGet, legacyServiceAccountsPath+"/"+id+"/tokens", nil, &wire); err != nil {
		return nil, err
	}
	tokens := make([]saToken, 0, len(wire))
	for _, token := range wire {
		if token.ID <= 0 {
			return nil, errors.New("grafana returned an invalid legacy service account token ID")
		}
		tokens = append(tokens, saToken{
			ID:         strconv.FormatInt(token.ID, 10),
			Title:      token.Name,
			Created:    token.Created,
			Expiration: token.Expiration,
			LastUsedAt: token.LastUsedAt,
			HasExpired: token.HasExpired || token.Expiration != nil && !token.Expiration.After(time.Now()),
		})
	}
	return tokens, nil
}

func (g *grafana) legacyDeleteToken(ctx context.Context, serviceAccountID, tokenID string) error {
	saID, err := legacyNumericID("service account", serviceAccountID)
	if err != nil {
		return err
	}
	id, err := legacyNumericID("service account token", tokenID)
	if err != nil {
		return err
	}
	path := legacyServiceAccountsPath + "/" + saID + "/tokens/" + id
	return g.do(ctx, http.MethodDelete, path, nil, nil)
}

func (g *grafana) legacyNewToken(ctx context.Context, serviceAccountID, tokenName string, ttl time.Duration) (issuedToken, error) {
	saID, err := legacyNumericID("service account", serviceAccountID)
	if err != nil {
		return issuedToken{}, err
	}
	body := struct {
		Name          string `json:"name"`
		SecondsToLive int64  `json:"secondsToLive"`
	}{Name: tokenName, SecondsToLive: int64(ttl.Seconds())}
	var out struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
		Key  string `json:"key"`
	}
	path := legacyServiceAccountsPath + "/" + saID + "/tokens"
	if err := g.do(ctx, http.MethodPost, path, body, &out); err != nil {
		var apiErr *apiError
		if errors.As(err, &apiErr) && apiErr.Status >= 400 && apiErr.Status < 500 {
			return issuedToken{}, err
		}
		return issuedToken{}, errors.Join(err, g.legacyCleanupUndeliveredToken(saID, tokenName))
	}
	if out.Key == "" {
		return issuedToken{}, errors.Join(errors.New("grafana returned an empty service account token"), g.legacyCleanupUndeliveredToken(saID, tokenName))
	}
	if out.Name != tokenName {
		return issuedToken{}, errors.Join(fmt.Errorf("%w: grafana returned an unexpected service account token name", errTokenReconciliation), g.legacyCleanupUndeliveredToken(saID, tokenName))
	}
	created := time.Now().UTC()
	expires := created.Add(ttl)
	return issuedToken{Secret: out.Key, Name: tokenName, Created: created, Expires: &expires}, nil
}

func (g *grafana) legacyCleanupUndeliveredToken(serviceAccountID, tokenName string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	tokens, err := g.legacyTokens(ctx, serviceAccountID)
	if err != nil {
		return fmt.Errorf("%w: %w", errTokenReconciliation, err)
	}
	matches := make([]saToken, 0, 1)
	for _, token := range tokens {
		if token.Title == tokenName {
			matches = append(matches, token)
		}
	}
	if len(matches) != 1 {
		return fmt.Errorf("%w: found %d legacy tokens with the requested name", errTokenReconciliation, len(matches))
	}
	if err := g.legacyDeleteToken(ctx, serviceAccountID, matches[0].ID); err != nil {
		return fmt.Errorf("%w: %w", errTokenReconciliation, err)
	}
	return nil
}
