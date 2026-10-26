package repository

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFileAtomicRestartAndWriterExclusion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.pb")
	s, e := OpenFile(path)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = OpenFile(path); e == nil {
		t.Fatal("two file writers admitted")
	}
	ctx := context.Background()
	if e = s.Save(ctx, []byte("one")); e != nil {
		t.Fatal(e)
	}
	if e = s.Save(ctx, bytes.Repeat([]byte("x"), MaxSnapshotBytes+1)); e == nil {
		t.Fatal("oversized snapshot saved")
	}
	b, e := s.Load(ctx)
	if e != nil || string(b) != "one" {
		t.Fatal("failed write replaced committed state")
	}
	s.Close()
	s, e = OpenFile(path)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	b, e = s.Load(ctx)
	if e != nil || string(b) != "one" {
		t.Fatal(e, string(b))
	}
}
func TestEtcdPersistenceAndWriterExclusion(t *testing.T) {
	endpoint := os.Getenv("SDNC_TEST_ETCD")
	if endpoint == "" {
		t.Skip("set SDNC_TEST_ETCD to an isolated real etcd endpoint")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	prefix := fmt.Sprintf("/sdnc-test/%d", time.Now().UnixNano())
	s, e := OpenEtcd(ctx, []string{endpoint}, prefix)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = OpenEtcd(ctx, []string{endpoint}, prefix); e == nil {
		t.Fatal("two etcd writers admitted")
	}
	if _, e = s.Load(ctx); e != nil {
		t.Fatal(e)
	}
	if e = s.Save(ctx, []byte("snapshot")); e != nil {
		t.Fatal(e)
	}
	s.Close()
	s, e = OpenEtcd(ctx, []string{endpoint}, prefix)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	b, e := s.Load(ctx)
	if e != nil || string(b) != "snapshot" {
		t.Fatal(e, string(b))
	}
	if e = s.Save(ctx, []byte("next")); e != nil {
		t.Fatal(e)
	}
}
