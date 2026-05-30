package queue

import (
	"testing"
	"time"
)

func TestBackoff_BoundedAndCapped(t *testing.T) {
	q := &Postgres{cfg: Config{BaseBackoff: 2 * time.Second, MaxBackoff: 60 * time.Second}}

	for attempts := 1; attempts <= 12; attempts++ {
		// expected ceiling for this attempt (mirrors the cap + overflow guard)
		d := q.cfg.BaseBackoff << (attempts - 1)
		if d <= 0 || d > q.cfg.MaxBackoff {
			d = q.cfg.MaxBackoff
		}
		for range 50 { // sample the jitter
			got := q.backoff(attempts)
			if got < d/2 || got > d {
				t.Fatalf("attempts=%d backoff=%v not in [%v, %v]", attempts, got, d/2, d)
			}
		}
	}
}
