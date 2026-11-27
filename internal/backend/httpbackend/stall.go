package httpbackend

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"
)

// errStalled marks a request apx gave up on. It deliberately doesn't wrap
// context.Canceled, which would read as the caller giving up.
var errStalled = errors.New("upstream stopped responding")

// stallWatch cancels a request whose provider stays silent for too long, and
// wraps the response body to see that data still arrives.
type stallWatch struct {
	body   io.ReadCloser
	ctx    context.Context
	cancel context.CancelCauseFunc
	timer  *time.Timer
	limit  time.Duration
}

func watchStall(parent context.Context, limit time.Duration) (context.Context, *stallWatch) {
	ctx, cancel := context.WithCancelCause(parent)
	w := &stallWatch{ctx: ctx, cancel: cancel, limit: limit}
	w.timer = time.AfterFunc(limit, func() {
		cancel(fmt.Errorf("%w for %s", errStalled, limit))
	})
	return ctx, w
}

// explain replaces the cancellation error of a stalled request with its reason.
func (w *stallWatch) explain(err error) error {
	if cause := context.Cause(w.ctx); errors.Is(cause, errStalled) {
		return cause
	}
	return err
}

func (w *stallWatch) Read(p []byte) (int, error) {
	n, err := w.body.Read(p)
	if n > 0 {
		w.timer.Reset(w.limit)
	}
	if err != nil && err != io.EOF {
		err = w.explain(err)
	}
	return n, err
}

func (w *stallWatch) stop() {
	w.timer.Stop()
	w.cancel(nil)
}

func (w *stallWatch) Close() error {
	err := w.body.Close()
	w.stop()
	return err
}
