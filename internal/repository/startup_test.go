package repository

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
)

func TestStartupWaitOnlyRetriesDefinitiveOwnershipConflict(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for _, failure := range []error{ErrConflict, ErrUncertain, errors.New("transport unavailable")} {
		calls := 0
		_, err := waitForWriter(ctx, func(context.Context) (*Etcd, error) { calls++; return nil, failure })
		if !errors.Is(err, failure) || calls != 1 {
			t.Fatalf("non-ownership error was retried: calls=%d error=%v", calls, err)
		}
	}
	calls := 0
	expected := &Etcd{}
	got, err := waitForWriter(ctx, func(context.Context) (*Etcd, error) {
		calls++
		if calls == 1 {
			return nil, fmt.Errorf("wrapped: %w", ErrWriterOwned)
		}
		return expected, nil
	})
	if err != nil || got != expected || calls != 2 {
		t.Fatalf("did not retry ownership conflict: %v calls=%d", err, calls)
	}
}

func TestStartupWaitRequiresDeadlineAndHonorsCanceledContext(t *testing.T) {
	calls := 0
	attempt := func(context.Context) (*Etcd, error) { calls++; return nil, ErrWriterOwned }
	if _, err := waitForWriter(context.Background(), attempt); err == nil {
		t.Fatal("unbounded startup wait accepted")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	cancel()
	if _, err := waitForWriter(ctx, attempt); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation ignored: %v", err)
	}
	if calls != 0 {
		t.Fatal("attempt made after canceled or unbounded request")
	}
}

func startupEtcd(t *testing.T) (*clientv3.Client, []string, string) {
	t.Helper()
	endpoint := os.Getenv("SDNC_TEST_ETCD")
	if endpoint == "" {
		t.Skip("set SDNC_TEST_ETCD to an isolated real etcd endpoint")
	}
	client, err := clientv3.New(clientv3.Config{Endpoints: []string{endpoint}, DialTimeout: 3 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	prefix := fmt.Sprintf("/sdnc-startup-test/%d", time.Now().UnixNano())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, _ = client.Delete(ctx, prefix, clientv3.WithPrefix())
		_ = client.Close()
	})
	return client, []string{endpoint}, prefix
}

func TestEtcdStartupWaitForAbandonedLeasePreservesSnapshot(t *testing.T) {
	client, endpoints, prefix := startupEtcd(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	lease, err := client.Grant(ctx, 15)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = client.Put(ctx, prefix+"/writer", "abandoned-controller", clientv3.WithLease(lease.ID)); err != nil {
		t.Fatal(err)
	}
	if _, err = client.Put(ctx, prefix+"/snapshot", "preserved-before-migration"); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	store, err := OpenEtcdWait(ctx, endpoints, prefix)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if time.Since(started) < 10*time.Second {
		t.Fatal("acquired before the old 15-second lease could naturally expire")
	}
	old, err := client.TimeToLive(ctx, lease.ID)
	if err != nil || old.TTL != -1 {
		t.Fatalf("old lease did not expire naturally: %v %+v", err, old)
	}
	data, err := store.Load(ctx)
	if err != nil || string(data) != "preserved-before-migration" {
		t.Fatalf("snapshot changed during startup wait: %v %q", err, data)
	}
	if err = store.Save(ctx, []byte("new-writer-committed")); err != nil {
		t.Fatal(err)
	}
}

func TestEtcdStartupWaitDoesNotTakeOverLiveWriter(t *testing.T) {
	client, endpoints, prefix := startupEtcd(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	active, err := OpenEtcd(ctx, endpoints, prefix)
	if err != nil {
		t.Fatal(err)
	}
	defer active.Close()
	before, err := client.Get(ctx, prefix+"/writer")
	if err != nil {
		t.Fatal(err)
	}
	waitCtx, stop := context.WithTimeout(ctx, 1200*time.Millisecond)
	defer stop()
	start := time.Now()
	other, err := OpenEtcdWait(waitCtx, endpoints, prefix)
	if other != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("live writer was taken over: %v", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("startup deadline was not bounded")
	}
	after, err := client.Get(ctx, prefix+"/writer")
	if err != nil {
		t.Fatal(err)
	}
	if len(before.Kvs) != 1 || len(after.Kvs) != 1 || before.Kvs[0].ModRevision != after.Kvs[0].ModRevision || before.Kvs[0].Lease != after.Kvs[0].Lease {
		t.Fatal("live writer lock was changed")
	}
	if _, err = active.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if err = active.Save(ctx, []byte("original-writer-still-works")); err != nil {
		t.Fatal(err)
	}
}

func TestEtcdStartupWaitCancellationLeavesOwnerIntact(t *testing.T) {
	client, endpoints, prefix := startupEtcd(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	active, err := OpenEtcd(ctx, endpoints, prefix)
	if err != nil {
		t.Fatal(err)
	}
	defer active.Close()
	waitCtx, stop := context.WithCancel(ctx)
	finished := make(chan error, 1)
	go func() {
		s, err := OpenEtcdWait(waitCtx, endpoints, prefix)
		if s != nil {
			_ = s.Close()
		}
		finished <- err
	}()
	time.Sleep(150 * time.Millisecond)
	start := time.Now()
	stop()
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("wrong cancellation result: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown did not promptly cancel startup wait")
	}
	if time.Since(start) > time.Second {
		t.Fatal("slow shutdown")
	}
	owner, err := client.Get(ctx, prefix+"/writer")
	if err != nil || len(owner.Kvs) != 1 || string(owner.Kvs[0].Value) != active.owner {
		t.Fatalf("shutdown disturbed active owner: %v", err)
	}
	if _, err = active.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if err = active.Save(ctx, []byte("owner-survives-cancel")); err != nil {
		t.Fatal(err)
	}
}
