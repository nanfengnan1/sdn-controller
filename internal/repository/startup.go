package repository

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// OpenEtcdWait waits only after a definitive writer-lock conflict. This keeps a
// restarted bundled etcd alive while an abandoned lease expires naturally. It
// never deletes the competing lock, revokes its lease, or retries an uncertain
// transport/storage result. The caller must provide a total startup deadline.
func OpenEtcdWait(ctx context.Context, endpoints []string, prefix string) (*Etcd, error) {
	return waitForWriter(ctx, func(ctx context.Context) (*Etcd, error) {
		return OpenEtcd(ctx, endpoints, prefix)
	})
}

func waitForWriter(ctx context.Context, attempt func(context.Context) (*Etcd, error)) (*Etcd, error) {
	if _, bounded := ctx.Deadline(); !bounded {
		return nil, errors.New("writer acquisition requires a startup deadline")
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		store, err := attempt(ctx)
		if !errors.Is(err, ErrWriterOwned) {
			return store, err
		}
		timer := time.NewTimer(500 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, fmt.Errorf("writer lease remains held at startup deadline or cancellation: %w", ctx.Err())
		case <-timer.C:
		}
	}
}
