// Package repository persists an atomically published, bounded controller snapshot.
package repository

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
)

const MaxSnapshotBytes = 1024 * 1024

var ErrConflict = errors.New("repository changed or writer lease lost")

// ErrWriterOwned is a definitive failed create-if-absent CAS, not an uncertain write.
var ErrWriterOwned = errors.New("another controller owns the writer lease")
var ErrUncertain = errors.New("repository write outcome unknown; reopen before further use")

type Store interface {
	Load(context.Context) ([]byte, error)
	Save(context.Context, []byte) error
	Close() error
	Lost() <-chan struct{}
}

type File struct {
	path string
	lock *os.File
	lost chan struct{}
	once sync.Once
}

func OpenFile(path string) (*File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	f, e := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	if e = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		f.Close()
		return nil, fmt.Errorf("controller state already has a writer: %w", e)
	}
	return &File{path: path, lock: f, lost: make(chan struct{})}, nil
}
func (f *File) Load(ctx context.Context) ([]byte, error) {
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	b, e := os.ReadFile(f.path)
	if os.IsNotExist(e) {
		return nil, nil
	}
	return b, e
}
func (f *File) Save(ctx context.Context, b []byte) error {
	select {
	case <-f.lost:
		return ErrUncertain
	default:
	}
	if e := ctx.Err(); e != nil {
		return e
	}
	if len(b) > MaxSnapshotBytes {
		return fmt.Errorf("snapshot exceeds %d bytes; archive old data or migrate storage", MaxSnapshotBytes)
	}
	tmp, e := os.CreateTemp(filepath.Dir(f.path), ".controller-*")
	if e != nil {
		return e
	}
	defer os.Remove(tmp.Name())
	if e = tmp.Chmod(0600); e == nil {
		_, e = tmp.Write(b)
	}
	if e == nil {
		e = tmp.Sync()
	}
	ce := tmp.Close()
	if e == nil {
		e = ce
	}
	if e != nil {
		return e
	}
	if e = os.Rename(tmp.Name(), f.path); e != nil {
		return e
	}
	d, e := os.Open(filepath.Dir(f.path))
	if e != nil {
		f.once.Do(func() { close(f.lost) })
		return fmt.Errorf("%w: sync renamed snapshot directory: %v", ErrUncertain, e)
	}
	defer d.Close()
	if e = d.Sync(); e != nil {
		f.once.Do(func() { close(f.lost) })
		return fmt.Errorf("%w: sync renamed snapshot directory: %v", ErrUncertain, e)
	}
	return nil
}
func (f *File) Close() error {
	_ = syscall.Flock(int(f.lock.Fd()), syscall.LOCK_UN)
	return f.lock.Close()
}
func (f *File) Lost() <-chan struct{} { return f.lost }

type Etcd struct {
	client              *clientv3.Client
	kv                  clientv3.KV
	key, lockKey, owner string
	rev                 int64
	lease               clientv3.LeaseID
	lost                chan struct{}
	once                sync.Once
	cancel              context.CancelFunc
}

func OpenEtcd(ctx context.Context, endpoints []string, prefix string) (*Etcd, error) {
	c, e := clientv3.New(clientv3.Config{Endpoints: endpoints, DialTimeout: 5 * time.Second})
	if e != nil {
		return nil, e
	}
	token := make([]byte, 16)
	if _, e = rand.Read(token); e != nil {
		c.Close()
		return nil, e
	}
	p := strings.TrimRight(prefix, "/")
	s := &Etcd{client: c, kv: c.KV, key: p + "/snapshot", lockKey: p + "/writer", owner: hex.EncodeToString(token), lost: make(chan struct{})}
	grant, e := c.Grant(ctx, 15)
	if e != nil {
		c.Close()
		return nil, e
	}
	s.lease = grant.ID
	r, e := c.Txn(ctx).If(clientv3.Compare(clientv3.CreateRevision(s.lockKey), "=", 0)).Then(clientv3.OpPut(s.lockKey, s.owner, clientv3.WithLease(s.lease))).Commit()
	if e != nil || !r.Succeeded {
		c.Revoke(ctx, s.lease)
		c.Close()
		if e == nil {
			e = ErrWriterOwned
		}
		return nil, e
	}
	keepCtx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	ch, e := c.KeepAlive(keepCtx, s.lease)
	if e != nil {
		s.Close()
		return nil, e
	}
	go func() {
		for range ch {
		}
		s.once.Do(func() { close(s.lost) })
	}()
	return s, nil
}
func (s *Etcd) Load(ctx context.Context) ([]byte, error) {
	r, e := s.kv.Get(ctx, s.key)
	if e != nil {
		return nil, e
	}
	if len(r.Kvs) == 0 {
		s.rev = 0
		return nil, nil
	}
	s.rev = r.Kvs[0].ModRevision
	return r.Kvs[0].Value, nil
}
func (s *Etcd) Save(ctx context.Context, b []byte) error {
	select {
	case <-s.lost:
		return ErrConflict
	default:
	}
	if e := ctx.Err(); e != nil {
		return e
	}
	if len(b) > MaxSnapshotBytes {
		return fmt.Errorf("snapshot exceeds %d bytes; bounded v1 store exhausted", MaxSnapshotBytes)
	}
	r, e := s.write(ctx, b)
	if e != nil {
		// Retrying the SAME guarded write is safe, even when the first request
		// is still in flight. A read alone cannot rule out a later commit.
		// Do not reuse a canceled caller context for this bounded resolution.
		recovery, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		r, e = s.write(recovery, b)
		if e != nil {
			s.once.Do(func() { close(s.lost) })
			return fmt.Errorf("%w: %v", ErrUncertain, e)
		}
	}
	if r.Succeeded {
		s.rev = r.Header.Revision
		return nil
	}
	// Both reads are in the failed CAS transaction, so ownership and data are
	// checked at one revision. This also handles an etcd client retry whose
	// first attempt committed but lost its reply.
	if len(r.Responses) == 2 {
		data, owner := r.Responses[0].GetResponseRange(), r.Responses[1].GetResponseRange()
		if data != nil && owner != nil && len(data.Kvs) == 1 && len(owner.Kvs) == 1 &&
			string(owner.Kvs[0].Value) == s.owner && data.Kvs[0].ModRevision > s.rev && bytes.Equal(data.Kvs[0].Value, b) {
			s.rev = data.Kvs[0].ModRevision
			return nil
		}
	}
	// Never rebase an old in-memory database onto unrelated persisted data.
	s.once.Do(func() { close(s.lost) })
	return ErrConflict
}
func (s *Etcd) write(ctx context.Context, b []byte) (*clientv3.TxnResponse, error) {
	return s.kv.Txn(ctx).
		If(clientv3.Compare(clientv3.ModRevision(s.key), "=", s.rev), clientv3.Compare(clientv3.Value(s.lockKey), "=", s.owner)).
		Then(clientv3.OpPut(s.key, string(b))).
		Else(clientv3.OpGet(s.key), clientv3.OpGet(s.lockKey)).Commit()
}
func (s *Etcd) Lost() <-chan struct{} { return s.lost }
func (s *Etcd) Close() error {
	if s.cancel != nil {
		s.cancel()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, _ = s.client.Revoke(ctx, s.lease)
	return s.client.Close()
}
