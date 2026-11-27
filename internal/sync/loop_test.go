package sync

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/rcss-labs/yamdrake/internal/github"
)

func TestNextWait(t *testing.T) {
	t.Parallel()
	interval := time.Minute
	rate := &github.RateLimitError{RetryAfter: 5 * time.Minute}
	cases := []struct {
		name string
		err  error
		want time.Duration
	}{
		{"no error", nil, interval},
		{"plain error", errors.New("boom"), interval},
		{"rate limit", rate, 5 * time.Minute},
		{"rate limit inside a joined error", errors.Join(errors.New("x"), fmt.Errorf("job 3: %w", rate)), 5 * time.Minute},
		{"rate limit without a delay", &github.RateLimitError{}, interval},
	}
	for _, c := range cases {
		if got := nextWait(c.err, interval); got != c.want {
			t.Errorf("%s: nextWait = %v, want %v", c.name, got, c.want)
		}
	}
}
