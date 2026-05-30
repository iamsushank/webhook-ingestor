package worker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"webhook-ingestor/internal/queue"
)

// fakeQueue records which lifecycle method a dispatch routed to.
type fakeQueue struct{ acked, failed, retried string }

func (f *fakeQueue) Claim(context.Context) (*queue.Job, error) { return nil, nil }
func (f *fakeQueue) Ack(_ context.Context, id string) error    { f.acked = id; return nil }
func (f *fakeQueue) Retry(_ context.Context, id string, _ int, _ string) error {
	f.retried = id
	return nil
}
func (f *fakeQueue) Fail(_ context.Context, id string, _ string) error { f.failed = id; return nil }
func (f *fakeQueue) Notify()                                           {}

type fakeHandler struct{ err error }

func (h fakeHandler) Handle(context.Context, *queue.Job) error { return h.err }

func TestDispatch_RoutesOutcome(t *testing.T) {
	discard := slog.New(slog.NewTextHandler(io.Discard, nil))
	job := &queue.Job{ID: "job-1"}

	cases := []struct {
		name string
		err  error
		pick func(*fakeQueue) string // id recorded on the expected method
	}{
		{"nil_acks", nil, func(f *fakeQueue) string { return f.acked }},
		{"permanent_fails", queue.Permanentf("bad input"), func(f *fakeQueue) string { return f.failed }},
		{"transient_retries", errors.New("boom"), func(f *fakeQueue) string { return f.retried }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			q := &fakeQueue{}
			p := NewPool(q, fakeHandler{err: c.err}, 1, discard)
			p.dispatch(context.Background(), job)

			if got := c.pick(q); got != job.ID {
				t.Errorf("job not routed to expected lifecycle method (q=%+v)", q)
			}
			n := 0
			for _, v := range []string{q.acked, q.failed, q.retried} {
				if v != "" {
					n++
				}
			}
			if n != 1 {
				t.Errorf("expected exactly one outcome, got %d (q=%+v)", n, q)
			}
		})
	}
}
