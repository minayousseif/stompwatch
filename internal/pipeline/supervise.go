package pipeline

import (
	"context"
	"time"
)

// supervise runs fn until it returns nil or ctx is done. If fn panics, or
// returns an error while ctx is still active, supervise calls onRestart with
// the cause, waits delay, and runs fn again.
func supervise(ctx context.Context, name string, delay time.Duration, onRestart func(name string, cause any), fn func(context.Context) error) error {
	for {
		err, cause := runProtected(ctx, fn)
		if cause == nil {
			if err == nil || ctx.Err() != nil {
				return err
			}
			cause = err
		}
		onRestart(name, cause)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
	}
}

// runProtected runs fn and turns a panic into a non-nil cause.
func runProtected(ctx context.Context, fn func(context.Context) error) (err error, cause any) {
	defer func() {
		if r := recover(); r != nil {
			cause = r
		}
	}()
	return fn(ctx), nil
}
