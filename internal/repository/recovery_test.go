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

// Faults are injected at the reply boundary of a REAL etcd transaction. They
// neither stop etcd nor affect other namespaces or the running lab controller.
type faultKV struct {
	clientv3.KV
	mode   string
	calls  int
	cancel context.CancelFunc
}

func (f *faultKV) Txn(ctx context.Context) clientv3.Txn {
	return &faultTxn{Txn: f.KV.Txn(ctx), f: f}
}

type faultTxn struct {
	clientv3.Txn
	f *faultKV
}

func (t *faultTxn) If(c ...clientv3.Cmp) clientv3.Txn  { t.Txn = t.Txn.If(c...); return t }
func (t *faultTxn) Then(o ...clientv3.Op) clientv3.Txn { t.Txn = t.Txn.Then(o...); return t }
func (t *faultTxn) Else(o ...clientv3.Op) clientv3.Txn { t.Txn = t.Txn.Else(o...); return t }
func (t *faultTxn) Commit() (*clientv3.TxnResponse, error) {
	t.f.calls++
	if t.f.mode == "unavailable" || t.f.calls == 1 && t.f.mode == "before-send" {
		return nil, errors.New("injected transport unavailable")
	}
	r, e := t.Txn.Commit()
	if e == nil && t.f.calls == 1 && (t.f.mode == "lost-reply" || t.f.mode == "canceled-caller") {
		if t.f.cancel != nil {
			t.f.cancel()
		}
		return nil, context.DeadlineExceeded
	}
	return r, e
}

func recoveryStore(t *testing.T) (*Etcd, context.Context) {
	t.Helper()
	endpoint := os.Getenv("SDNC_TEST_ETCD")
	if endpoint == "" {
		t.Skip("set SDNC_TEST_ETCD to an isolated real etcd endpoint")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	prefix := fmt.Sprintf("/sdnc-recovery-test/%d", time.Now().UnixNano())
	s, e := OpenEtcd(ctx, []string{endpoint}, prefix)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 3*time.Second)
		defer done()
		_, _ = s.client.Delete(cleanup, prefix+"/", clientv3.WithPrefix())
		_ = s.Close()
	})
	if _, e = s.Load(ctx); e != nil {
		t.Fatal(e)
	}
	if e = s.Save(ctx, []byte("baseline")); e != nil {
		t.Fatal(e)
	}
	return s, ctx
}

func TestEtcdAmbiguousWriteRecovery(t *testing.T) {
	for _, mode := range []string{"before-send", "lost-reply", "canceled-caller"} {
		t.Run(mode, func(t *testing.T) {
			s, ctx := recoveryStore(t)
			caller, cancel := context.WithCancel(ctx)
			defer cancel()
			fault := &faultKV{KV: s.client.KV, mode: mode, cancel: cancel}
			s.kv = fault
			if e := s.Save(caller, []byte("accepted-job")); e != nil {
				t.Fatal(e)
			}
			if fault.calls != 2 {
				t.Fatalf("expected bounded resolution, got %d attempts", fault.calls)
			}
			select {
			case <-s.Lost():
				t.Fatal("resolved write stopped store")
			default:
			}
			// A successful resolution must also permit the NEXT write, without
			// restarting or manually resetting the CAS revision.
			if e := s.Save(ctx, []byte("task-progress")); e != nil {
				t.Fatal(e)
			}
			got, e := s.client.Get(ctx, s.key)
			if e != nil || len(got.Kvs) != 1 || string(got.Kvs[0].Value) != "task-progress" {
				t.Fatalf("subsequent write not durable: %v, %v", got, e)
			}
		})
	}
}

func TestEtcdUnresolvedWriteStopsWriter(t *testing.T) {
	s, ctx := recoveryStore(t)
	fault := &faultKV{KV: s.client.KV, mode: "unavailable"}
	s.kv = fault
	if e := s.Save(ctx, []byte("uncertain")); !errors.Is(e, ErrUncertain) {
		t.Fatalf("got %v", e)
	}
	select {
	case <-s.Lost():
	default:
		t.Fatal("unsafe writer remained active")
	}
	if e := s.Save(ctx, []byte("must-not-write")); e == nil {
		t.Fatal("write admitted after unknown outcome")
	}
	if fault.calls != 2 {
		t.Fatalf("unbounded retry: %d", fault.calls)
	}
}

func TestEtcdConflictingStateStopsWriter(t *testing.T) {
	for _, conflict := range []string{"snapshot", "owner"} {
		t.Run(conflict, func(t *testing.T) {
			s, ctx := recoveryStore(t)
			if conflict == "snapshot" {
				if _, e := s.client.Put(ctx, s.key, "external-write"); e != nil {
					t.Fatal(e)
				}
			} else {
				if _, e := s.client.Put(ctx, s.lockKey, "another-owner"); e != nil {
					t.Fatal(e)
				}
			}
			if e := s.Save(ctx, []byte("old-memory-write")); !errors.Is(e, ErrConflict) {
				t.Fatalf("got %v", e)
			}
			select {
			case <-s.Lost():
			default:
				t.Fatal("conflicting writer remained active")
			}
			got, e := s.client.Get(ctx, s.key)
			if e != nil {
				t.Fatal(e)
			}
			want := "baseline"
			if conflict == "snapshot" {
				want = "external-write"
			}
			if string(got.Kvs[0].Value) != want {
				t.Fatal("conflicting data overwritten")
			}
		})
	}
}

func TestEtcdCanceledBeforeWriteDoesNotStopWriter(t *testing.T) {
	s, ctx := recoveryStore(t)
	caller, cancel := context.WithCancel(ctx)
	cancel()
	if e := s.Save(caller, []byte("not-sent")); !errors.Is(e, context.Canceled) {
		t.Fatalf("got %v", e)
	}
	if e := s.Save(ctx, []byte("next")); e != nil {
		t.Fatal(e)
	}
}
