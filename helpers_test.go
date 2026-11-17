// Shared test helpers: tiny assertions and file fixtures.
package main

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// t0 is the fixed starting time for tests that control the clock.
var t0 = time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)

const testURL = "https://example.com/x"

var ctx = context.Background()

// in returns the time d from now.
func in(d time.Duration) time.Time { return time.Now().Add(d) }

// near asserts that two times differ by less than five seconds. The margin
// absorbs the time a test takes and expiries kept in whole seconds.
func near(t *testing.T, got, want time.Time) {
	t.Helper()
	if diff := got.Sub(want).Abs(); diff > 5*time.Second {
		t.Fatalf("time %v is %v away from %v", got, diff, want)
	}
}

// eq fails the test when got differs from want.
func eq[T comparable](t *testing.T, got, want T) {
	t.Helper()
	if got != want {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// must fails the test when err is not nil.
func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// wantErr fails the test unless err mentions substr.
func wantErr(t *testing.T, err error, substr string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected an error containing %q, got nil", substr)
	}
	if !strings.Contains(err.Error(), substr) {
		t.Fatalf("error %q does not contain %q", err, substr)
	}
}

// captureLog sends what the code under test logs to a buffer until the test
// ends, so that errors a test provokes stay out of the test output, and
// returns the buffer.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	return &buf
}

// writeFile creates dir/name with content and returns its path.
func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	must(t, os.WriteFile(path, []byte(content), 0o644))
	return path
}
