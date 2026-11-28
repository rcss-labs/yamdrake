package versions_test

import (
	"slices"
	"testing"

	"github.com/rcss-labs/yumdrake/internal/versions"
)

func TestCompareSemver(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"1.4.2", "1.4.10", -1},
		{"1.4.10", "1.4.2", 1},
		{"v2.0.0", "2.0.0", 0},
		{"1.5.0-rc1", "1.5.0", -1},
		{"1.5.0", "1.5.0-rc1", 1},
		{"1.5.0-rc1", "1.5.0-rc2", -1},
		{"1.5.0-rc2", "1.5.0-rc1", 1},
		{"1.5.0-alpha", "1.5.0-1", 1},
		{"2.0.0", "1.99.99", 1},
		{"latest", "1.0.0", -1},
		{"1.0.0", "latest", 1},
		{"latest", "9f8e7d6c", 0},
		{"latest", "latest", 0},
	}
	for _, tt := range tests {
		t.Run(tt.a+" vs "+tt.b, func(t *testing.T) {
			if got := versions.CompareSemver(tt.a, tt.b); got != tt.want {
				t.Errorf("CompareSemver(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
			}
		})
	}

	t.Run("non-versions sort last and stay in order", func(t *testing.T) {
		tags := []string{"latest", "1.0.0", "abc1234", "2.0.0", "main"}
		slices.SortStableFunc(tags, func(a, b string) int { return -versions.CompareSemver(a, b) })
		want := []string{"2.0.0", "1.0.0", "latest", "abc1234", "main"}
		if !slices.Equal(tags, want) {
			t.Errorf("sorted newest first = %v, want %v", tags, want)
		}
	})
}
