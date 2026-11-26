package erasure

import (
	"testing"
	"time"
)

func TestPhotoBusy(t *testing.T) {
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	quiet := PhotoQuiet{UploadsBefore: now.Add(-20 * time.Minute), ProcessingBefore: now.Add(-10 * time.Minute)}
	fresh, old := now.Add(-time.Minute), now.Add(-time.Hour)
	for _, c := range []struct {
		name       string
		state      string
		created    time.Time
		processing *time.Time
		busy       bool
	}{
		{"processed", "ready", fresh, nil, false},
		{"needs location", "needs-location", fresh, nil, false},
		{"failed", "failed", fresh, nil, false},
		{"pending, upload URL still valid", "pending", fresh, nil, true},
		{"pending, upload URL expired", "pending", old, nil, false},
		{"processing now", "processing", old, &fresh, true},
		{"processing, stale", "processing", old, &old, false},
		{"processing without a start time", "processing", old, nil, true},
	} {
		if got := photoBusy(c.state, c.created, c.processing, quiet); got != c.busy {
			t.Errorf("%s: busy=%v, want %v", c.name, got, c.busy)
		}
	}
}
