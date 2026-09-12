package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"sync"
	"time"
)

type mutationGuard interface {
	Check() error
	Release()
}

const leaseTimeFormat = "2006-01-02T15:04:05.000000Z07:00"

type mutationLocker interface {
	Acquire(context.Context, string) (mutationGuard, error)
}

func newMutationLocker(cfg Config) (mutationLocker, error) {
	if cfg.RotationLockMode == "kubernetes" {
		return newLeaseLocker(cfg)
	}
	return newLocalLocker(), nil
}

type localLocker struct {
	mu    sync.Mutex
	locks map[string]*localLock
}

type localLock struct {
	ready chan struct{}
	refs  int
}

type localGuard struct {
	once   sync.Once
	locker *localLocker
	key    string
	lock   *localLock
}

func newLocalLocker() *localLocker { return &localLocker{locks: make(map[string]*localLock)} }

func (l *localLocker) Acquire(ctx context.Context, key string) (mutationGuard, error) {
	l.mu.Lock()
	lock := l.locks[key]
	if lock == nil {
		lock = &localLock{ready: make(chan struct{}, 1)}
		lock.ready <- struct{}{}
		l.locks[key] = lock
	}
	lock.refs++
	l.mu.Unlock()

	select {
	case <-ctx.Done():
		l.releaseRef(key, lock, false)
		return nil, ctx.Err()
	case <-lock.ready:
		return &localGuard{locker: l, key: key, lock: lock}, nil
	}
}

func (l *localLocker) releaseRef(key string, lock *localLock, unlock bool) {
	if unlock {
		lock.ready <- struct{}{}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	lock.refs--
	if lock.refs == 0 {
		delete(l.locks, key)
	}
}

func (g *localGuard) Check() error { return nil }
func (g *localGuard) Release() {
	g.once.Do(func() { g.locker.releaseRef(g.key, g.lock, true) })
}

type leaseLocker struct {
	client        *http.Client
	baseURL       string
	leaseName     string
	namespace     string
	podName       string
	tokenFile     string
	leaseDuration time.Duration
	now           func() time.Time
}

func (l *leaseLocker) ready(ctx context.Context) error {
	_, err := l.readLease(ctx, true)
	return err
}

func (l *leaseLocker) readLease(ctx context.Context, create bool) (kubeLease, error) {
	var lease kubeLease
	status, err := l.request(ctx, http.MethodGet, nil, &lease)
	if status != http.StatusNotFound || !create {
		return lease, err
	}
	initial := map[string]any{
		"apiVersion": "coordination.k8s.io/v1",
		"kind":       "Lease",
		"metadata":   map[string]string{"name": l.leaseName, "namespace": l.namespace},
		"spec":       map[string]any{},
	}
	status, err = l.request(ctx, http.MethodPost, initial, nil)
	if err != nil && status != http.StatusConflict {
		return lease, err
	}
	// Another pod may have created and acquired the Lease before this pod.
	_, err = l.request(ctx, http.MethodGet, nil, &lease)
	return lease, err
}

type kubeLease struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Metadata   struct {
		Name            string            `json:"name"`
		Namespace       string            `json:"namespace"`
		ResourceVersion string            `json:"resourceVersion"`
		UID             string            `json:"uid,omitempty"`
		Labels          map[string]string `json:"labels,omitempty"`
		Annotations     map[string]string `json:"annotations,omitempty"`
		OwnerReferences []json.RawMessage `json:"ownerReferences,omitempty"`
		Finalizers      []string          `json:"finalizers,omitempty"`
	} `json:"metadata"`
	Spec struct {
		HolderIdentity       string `json:"holderIdentity,omitempty"`
		LeaseDurationSeconds int32  `json:"leaseDurationSeconds,omitempty"`
		AcquireTime          string `json:"acquireTime,omitempty"`
		RenewTime            string `json:"renewTime,omitempty"`
		LeaseTransitions     int32  `json:"leaseTransitions,omitempty"`
	} `json:"spec"`
}

type leaseGuard struct {
	locker      *leaseLocker
	holder      string
	stop        chan struct{}
	done        chan struct{}
	once        sync.Once
	mu          sync.RWMutex
	lost        error
	lastRenew   time.Time
	cancelRenew context.CancelFunc
}

func newLeaseLocker(cfg Config) (*leaseLocker, error) {
	host := os.Getenv("KUBERNETES_SERVICE_HOST")
	port := env("KUBERNETES_SERVICE_PORT", "443")
	if host == "" {
		return nil, errors.New("KUBERNETES_SERVICE_HOST is required in kubernetes lock mode")
	}
	caPEM, err := os.ReadFile(cfg.RotationCAFile)
	if err != nil {
		return nil, fmt.Errorf("reading Kubernetes CA: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		return nil, errors.New("kubernetes CA contains no certificates")
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}}
	return &leaseLocker{
		client:        &http.Client{Timeout: 5 * time.Second, Transport: transport},
		baseURL:       "https://" + net.JoinHostPort(host, port),
		leaseName:     cfg.RotationLease,
		namespace:     cfg.PodNamespace,
		podName:       cfg.PodName,
		tokenFile:     cfg.RotationTokenFile,
		leaseDuration: 15 * time.Second,
		now:           time.Now,
	}, nil
}

func (l *leaseLocker) path() string {
	return l.collectionPath() + "/" + url.PathEscape(l.leaseName)
}

func (l *leaseLocker) collectionPath() string {
	return "/apis/coordination.k8s.io/v1/namespaces/" + url.PathEscape(l.namespace) + "/leases"
}

func (l *leaseLocker) request(ctx context.Context, method string, body any, out any) (int, error) {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		reader = bytes.NewReader(b)
	}
	token, err := os.ReadFile(l.tokenFile) // #nosec G304 G703 -- fixed projected-token path from configuration.
	if err != nil {
		return 0, fmt.Errorf("reading Kubernetes token: %w", err)
	}
	reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	path := l.path()
	if method == http.MethodPost {
		path = l.collectionPath()
	}
	req, err := http.NewRequestWithContext(reqCtx, method, l.baseURL+path, reader) // #nosec G704 -- Kubernetes supplies the API host; no request input controls it.
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+string(bytes.TrimSpace(token)))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := l.client.Do(req) // #nosec G704 -- the destination is the configured Kubernetes API.
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return resp.StatusCode, fmt.Errorf("kubernetes Lease %s returned HTTP %d", method, resp.StatusCode)
	}
	if out != nil {
		if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out); err != nil {
			return resp.StatusCode, err
		}
	}
	return resp.StatusCode, nil
}

func randomHolder(pod string) (string, error) {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return pod + "-" + hex.EncodeToString(b), nil
}

func (l *leaseLocker) Acquire(ctx context.Context, _ string) (mutationGuard, error) {
	holder, err := randomHolder(l.podName)
	if err != nil {
		return nil, err
	}
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		attemptedAt := l.now()
		acquired, err := l.update(ctx, holder, true)
		if err == nil && acquired {
			renewCtx, cancelRenew := context.WithCancel(context.Background())
			g := &leaseGuard{locker: l, holder: holder, stop: make(chan struct{}), done: make(chan struct{}), lastRenew: attemptedAt, cancelRenew: cancelRenew}
			go g.renew(renewCtx) // #nosec G118 -- Release cancels and joins renewal after the guarded operation.
			return g, nil
		}
		if err != nil {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

func (l *leaseLocker) update(ctx context.Context, holder string, acquire bool) (bool, error) {
	lease, err := l.readLease(ctx, acquire)
	if err != nil {
		return false, err
	}
	now := l.now().UTC()
	expires := time.Time{}
	if lease.Spec.RenewTime != "" {
		parsed, err := time.Parse(leaseTimeFormat, lease.Spec.RenewTime)
		if err != nil {
			return false, fmt.Errorf("parsing Lease renewTime: %w", err)
		}
		expires = parsed.Add(time.Duration(lease.Spec.LeaseDurationSeconds) * time.Second)
	}
	ours := lease.Spec.HolderIdentity == holder
	available := lease.Spec.HolderIdentity == "" || !expires.After(now)
	if !acquire && !ours {
		return false, nil
	}
	if !ours && !available {
		return false, nil
	}
	if !ours {
		lease.Spec.LeaseTransitions++
		lease.Spec.AcquireTime = now.Format(leaseTimeFormat)
	}
	lease.Spec.HolderIdentity = holder
	lease.Spec.LeaseDurationSeconds = int32(l.leaseDuration / time.Second) // #nosec G115 -- the internal duration is fixed at 15 seconds.
	lease.Spec.RenewTime = now.Format(leaseTimeFormat)
	status, err := l.request(ctx, http.MethodPut, lease, &kubeLease{})
	if status == http.StatusConflict && acquire {
		return false, nil
	}
	return err == nil, err
}

func (g *leaseGuard) renew(ctx context.Context) {
	defer close(g.done)
	ticker := time.NewTicker(g.locker.leaseDuration / 3)
	defer ticker.Stop()
	for {
		select {
		case <-g.stop:
			return
		case <-ticker.C:
			g.mu.RLock()
			expired := !g.lastRenew.Add(g.locker.leaseDuration).After(g.locker.now())
			g.mu.RUnlock()
			if expired {
				g.mu.Lock()
				g.lost = errors.New("kubernetes rotation Lease expired before renewal")
				g.mu.Unlock()
				return
			}
			attemptedAt := g.locker.now()
			ok, err := g.locker.update(ctx, g.holder, false)
			if err != nil || !ok {
				g.mu.Lock()
				g.lost = errors.New("kubernetes rotation Lease was lost")
				g.mu.Unlock()
				return
			}
			g.mu.Lock()
			g.lastRenew = attemptedAt
			g.mu.Unlock()
		}
	}
}

func (g *leaseGuard) Check() error {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if g.lost == nil && !g.lastRenew.Add(g.locker.leaseDuration).After(g.locker.now()) {
		return errors.New("kubernetes rotation Lease expired")
	}
	return g.lost
}

func (g *leaseGuard) Release() {
	g.once.Do(func() {
		close(g.stop)
		g.cancelRenew()
		<-g.done
		var lease kubeLease
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if _, err := g.locker.request(ctx, http.MethodGet, nil, &lease); err != nil || lease.Spec.HolderIdentity != g.holder {
			return
		}
		lease.Spec.HolderIdentity = ""
		lease.Spec.RenewTime = g.locker.now().UTC().Format(leaseTimeFormat)
		_, _ = g.locker.request(ctx, http.MethodPut, lease, nil)
	})
}
