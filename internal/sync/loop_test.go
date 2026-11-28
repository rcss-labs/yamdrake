package sync

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/rcss-labs/yumdrake/internal/github"
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
		{"rate limit shorter than the interval", &github.RateLimitError{RetryAfter: time.Second}, interval},
		{"rate limit far in the future is capped", &github.RateLimitError{RetryAfter: 3 * time.Hour}, maxRateLimitWait},
	}
	for _, c := range cases {
		if got := nextWait(c.err, interval); got != c.want {
			t.Errorf("%s: nextWait = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestNextWaitNeverShortensALongInterval(t *testing.T) {
	t.Parallel()
	interval := time.Hour
	if got := nextWait(&github.RateLimitError{RetryAfter: 5 * time.Minute}, interval); got != interval {
		t.Errorf("nextWait = %v, want the configured %v", got, interval)
	}
}

func TestTruncateMessageKeepsValidUTF8(t *testing.T) {
	t.Parallel()
	message := "aé" // 'é' is two bytes; cutting at 2 splits it
	if got := truncateMessage(message, 2); got != "a" {
		t.Fatalf("truncateMessage = %q, want %q", got, "a")
	}
	if got := truncateMessage(message, 3); got != message {
		t.Fatalf("truncateMessage = %q, want unchanged", got)
	}
}

func TestSourceErrorMatchesErrSourceAndKeepsItsCause(t *testing.T) {
	e := &Engine{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	cause := &github.RateLimitError{RetryAfter: time.Minute}
	err := e.sourceError("read job branch head", cause)
	if !errors.Is(err, ErrSource) {
		t.Errorf("source error does not match ErrSource: %v", err)
	}
	var limited *github.RateLimitError
	if !errors.As(err, &limited) {
		t.Errorf("source error lost its cause: %v", err)
	}
	if !strings.HasPrefix(err.Error(), "read job branch head: ") {
		t.Errorf("message = %q, want the step first", err.Error())
	}
	if errors.Is(errors.New("database is down"), ErrSource) {
		t.Error("an unrelated error matches ErrSource")
	}
}
