package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestLocalLockSerializesTheSameIdentity(t *testing.T) {
	locker := newLocalLocker()
	first, err := locker.Acquire(context.Background(), "same@example.com")
	if err != nil {
		t.Fatal(err)
	}
	acquired := make(chan mutationGuard, 1)
	go func() {
		guard, _ := locker.Acquire(context.Background(), "same@example.com")
		acquired <- guard
	}()
	select {
	case <-acquired:
		t.Fatal("same identity acquired the lock twice")
	case <-time.After(20 * time.Millisecond):
	}
	other, err := locker.Acquire(context.Background(), "other@example.com")
	if err != nil {
		t.Fatal(err)
	}
	other.Release()
	first.Release()
	select {
	case second := <-acquired:
		second.Release()
	case <-time.After(time.Second):
		t.Fatal("waiter did not acquire the released lock")
	}
}

type fakeLeaseAPI struct {
	mu    sync.Mutex
	lease kubeLease
}

func (f *fakeLeaseAPI) handler(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer projected" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	switch r.Method {
	case http.MethodGet:
		_ = json.NewEncoder(w).Encode(f.lease)
	case http.MethodPut:
		var next kubeLease
		if err := json.NewDecoder(r.Body).Decode(&next); err != nil {
			http.Error(w, "bad JSON", http.StatusBadRequest)
			return
		}
		for _, stamp := range []string{next.Spec.AcquireTime, next.Spec.RenewTime} {
			if _, err := time.Parse("2006-01-02T15:04:05.000000Z07:00", stamp); err != nil {
				http.Error(w, "Lease requires Kubernetes MicroTime", http.StatusBadRequest)
				return
			}
		}
		f.lease = next
		_ = json.NewEncoder(w).Encode(f.lease)
	default:
		http.Error(w, "method", http.StatusMethodNotAllowed)
	}
}

func testLeaseLocker(t *testing.T, api *fakeLeaseAPI, pod string) *leaseLocker {
	t.Helper()
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("projected\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(http.HandlerFunc(api.handler))
	t.Cleanup(ts.Close)
	return &leaseLocker{client: ts.Client(), baseURL: ts.URL, leaseName: "rotation", namespace: "default", podName: pod, tokenFile: tokenFile, leaseDuration: 2 * time.Second, now: time.Now}
}

func TestKubernetesLeaseSerializesPodsAndDetectsLoss(t *testing.T) {
	api := &fakeLeaseAPI{}
	api.lease.APIVersion = "coordination.k8s.io/v1"
	api.lease.Kind = "Lease"
	api.lease.Metadata.Name = "rotation"
	api.lease.Metadata.Namespace = "default"
	api.lease.Metadata.Labels = map[string]string{"app.kubernetes.io/managed-by": "Helm"}
	api.lease.Metadata.Annotations = map[string]string{"argocd.argoproj.io/tracking-id": "test:coordination.k8s.io/Lease:default/rotation"}
	firstLocker := testLeaseLocker(t, api, "pod-one")
	secondLocker := testLeaseLocker(t, api, "pod-two")
	first, err := firstLocker.Acquire(context.Background(), "ignored")
	if err != nil {
		t.Fatal(err)
	}
	api.mu.Lock()
	preserved := api.lease.Metadata.Labels["app.kubernetes.io/managed-by"] == "Helm" && api.lease.Metadata.Annotations["argocd.argoproj.io/tracking-id"] != ""
	api.mu.Unlock()
	if !preserved {
		t.Fatal("Lease update discarded deployment ownership metadata")
	}
	blockedCtx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	if _, err := secondLocker.Acquire(blockedCtx, "ignored"); err == nil {
		t.Fatal("a second pod acquired a live Lease")
	}

	api.mu.Lock()
	api.lease.Spec.HolderIdentity = "pod-three-stole-it"
	api.lease.Spec.RenewTime = time.Now().UTC().Format(leaseTimeFormat)
	api.mu.Unlock()
	deadline := time.Now().Add(time.Second)
	for first.Check() == nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if first.Check() == nil {
		t.Fatal("the guard did not report a lost Lease")
	}
	first.Release()
}

func TestKubernetesLeaseSerializesMicroTimeWithSixDigits(t *testing.T) {
	api := &fakeLeaseAPI{}
	locker := testLeaseLocker(t, api, "pod-one")
	instant := time.Date(2026, time.September, 12, 8, 9, 10, 123456789, time.UTC)
	locker.now = func() time.Time { return instant }

	acquired, err := locker.update(context.Background(), "pod-one-holder", true)
	if err != nil {
		t.Fatal(err)
	}
	if !acquired {
		t.Fatal("Lease was not acquired")
	}
	api.mu.Lock()
	defer api.mu.Unlock()
	const want = "2026-09-12T08:09:10.123456Z"
	if api.lease.Spec.AcquireTime != want || api.lease.Spec.RenewTime != want {
		t.Fatalf("Lease timestamps = %q, %q; want %q", api.lease.Spec.AcquireTime, api.lease.Spec.RenewTime, want)
	}
}

func TestLeaseGuardRejectsAResumeAfterItsLocalDeadline(t *testing.T) {
	start := time.Unix(1_700_000_000, 0)
	locker := &leaseLocker{leaseDuration: 15 * time.Second, now: func() time.Time { return start.Add(16 * time.Second) }}
	guard := &leaseGuard{locker: locker, lastRenew: start}
	if err := guard.Check(); err == nil {
		t.Fatal("an expired local Lease deadline still allowed mutation")
	}
}
