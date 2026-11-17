// Tests for the logs: their tab-separated lines, daily files, the error
// log's slog handler, compression and removal of old days, and file modes.
package app

import (
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// logTime is when most log tests write: 08:36:36 on 2026-11-13, UTC.
var logTime = time.Date(2026, 11, 13, 8, 36, 36, 0, time.UTC)

// testClock is a clock tests move by hand.
type testClock struct{ now time.Time }

func (c *testClock) Now() time.Time { return c.now }

// openLogs opens logs in dir on clock with the default days.
func openLogs(t *testing.T, dir string, clock *testClock) *Logs {
	t.Helper()
	logs, err := OpenLogs(LogConfig{Enabled: true, Dir: dir, PlainDays: 7, ArchiveDays: 30}, clock.Now)
	must(t, err)
	t.Cleanup(func() { logs.Close() })
	return logs
}

// logTo gives s logs in a new folder on the real clock and returns the
// folder.
func logTo(t *testing.T, s *Server) string {
	t.Helper()
	dir := t.TempDir()
	logs, err := OpenLogs(LogConfig{Enabled: true, Dir: dir, PlainDays: 7, ArchiveDays: 30}, time.Now)
	must(t, err)
	t.Cleanup(func() { logs.Close() })
	s.logs = logs
	return dir
}

// logLines returns the lines of one log file, or nil if it does not exist.
func logLines(t *testing.T, dir, name string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name))
	if os.IsNotExist(err) {
		return nil
	}
	must(t, err)
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

// withoutTime checks that every line starts with the time and a tab, and
// returns the lines without them.
func withoutTime(t *testing.T, lines []string) []string {
	t.Helper()
	for i, line := range lines {
		clock, rest, ok := strings.Cut(line, "\t")
		if _, err := time.Parse("15:04:05", clock); !ok || len(clock) != 8 || err != nil {
			t.Fatalf("line %q does not start with the time", line)
		}
		lines[i] = rest
	}
	return lines
}

// loggedLines returns today's lines of one log ("access", "error" or
// "admin") in dir, without their time.
func loggedLines(t *testing.T, dir, log string) []string {
	t.Helper()
	return withoutTime(t, logLines(t, dir, log+"-"+time.Now().UTC().Format(dayLayout)+".log"))
}

// exists reports whether dir/name exists.
func exists(dir, name string) bool {
	_, err := os.Stat(filepath.Join(dir, name))
	return err == nil
}

// gunzip returns the uncompressed content of a gzip file.
func gunzip(t *testing.T, path string) string {
	t.Helper()
	f, err := os.Open(path)
	must(t, err)
	defer f.Close()
	r, err := gzip.NewReader(f)
	must(t, err)
	data, err := io.ReadAll(r)
	must(t, err)
	return string(data)
}

func TestLogLine(t *testing.T) {
	dir := t.TempDir()
	clock := &testClock{now: time.Date(2026, 11, 13, 8, 36, 36, 0, time.UTC)}
	logs, err := OpenLogs(LogConfig{Enabled: true, Dir: dir, PlainDays: 7, ArchiveDays: 30}, clock.Now)
	must(t, err)
	logs.Access.Line("create", "short.example", "aB3xK", "203.0.113.7", "1")
	logs.Access.Line("redirect", "short.example", "aB3xK", "203.0.113.7", "")
	must(t, logs.Close())

	data, err := os.ReadFile(filepath.Join(dir, "access-2026-11-13.log"))
	must(t, err)
	eq(t, string(data), "08:36:36\tcreate\tshort.example\taB3xK\t203.0.113.7\t1\n"+
		"08:36:36\tredirect\tshort.example\taB3xK\t203.0.113.7\t\n")
}

// TestLogLineEscapesEveryField: whatever a field holds, a line stays one
// line with one tab between fields.
func TestLogLineEscapesEveryField(t *testing.T) {
	dir := t.TempDir()
	logs := openLogs(t, dir, &testClock{now: logTime})
	logs.Access.Line("create", "short.example\tforged", "aB3xK\nforged", "203.0.113.7\r\x00")
	must(t, logs.Close())

	eq(t, strings.Join(logLines(t, dir, "access-2026-11-13.log"), "\n"),
		"08:36:36\tcreate\tshort.example\\tforged\taB3xK\\nforged\t203.0.113.7\\r\\x00")
}

func TestEscapeField(t *testing.T) {
	eq(t, escapeField("plain text"), "plain text")
	eq(t, escapeField("a\tb\nc\\d\re\x01f\x7f"), `a\tb\nc\\d\re\x01f\x7f`)
	eq(t, escapeField("o'zbek · кирилл"), "o'zbek · кирилл") // other text is written as it is
}

// TestLogRotatesDaily: a file is created at the first line of its day,
// named after the UTC date, and the time is UTC too.
func TestLogRotatesDaily(t *testing.T) {
	dir := t.TempDir()
	clock := &testClock{now: logTime}
	logs := openLogs(t, dir, clock)
	eq(t, exists(dir, "access-2026-11-13.log"), false) // no line yet
	logs.Access.Line("create")

	// 01:00 five hours east of UTC is still the 13th in UTC.
	clock.now = time.Date(2026, 11, 14, 1, 0, 0, 0, time.FixedZone("", 5*3600))
	logs.Access.Line("update")
	clock.now = time.Date(2026, 11, 14, 0, 0, 1, 0, time.UTC)
	logs.Access.Line("delete")
	must(t, logs.Close())

	eq(t, strings.Join(logLines(t, dir, "access-2026-11-13.log"), "\n"), "08:36:36\tcreate\n20:00:00\tupdate")
	eq(t, strings.Join(logLines(t, dir, "access-2026-11-14.log"), "\n"), "00:00:01\tdelete")
}

// TestLogIsPrivate: the access log holds the codes of secured links and
// the addresses of clients, so only the user qisqa runs as may read it, in
// its text files and its archives.
func TestLogIsPrivate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs")
	clock := &testClock{now: logTime}
	logs := openLogs(t, dir, clock)
	logs.Access.Line("create")

	mode := func(name string) os.FileMode {
		t.Helper()
		info, err := os.Stat(filepath.Join(dir, name))
		must(t, err)
		return info.Mode().Perm()
	}
	eq(t, mode(""), os.FileMode(0o700))
	eq(t, mode("access-2026-11-13.log"), os.FileMode(0o600))

	// The archive is private even when a left-over .tmp of any mode is in
	// its way.
	writeFile(t, dir, "access-2026-11-13.log.gz.tmp", "half written") // 0644
	clock.now = logTime.AddDate(0, 0, 7)
	must(t, logs.Tidy())
	eq(t, mode("access-2026-11-13.log.gz"), os.FileMode(0o600))
}

func TestAdminLog(t *testing.T) {
	dir := t.TempDir()
	logs := openLogs(t, dir, &testClock{now: logTime})
	logs.Admin.Line("ok", "account", "add", "Ali")
	must(t, logs.Close())

	data, err := os.ReadFile(filepath.Join(dir, "admin-2026-11-13.log"))
	must(t, err)
	eq(t, string(data), "08:36:36\tok\taccount\tadd\tAli\n")
}

func TestErrorLog(t *testing.T) {
	dir := t.TempDir()
	logs := openLogs(t, dir, &testClock{now: logTime})
	logger := slog.New(logs.Handler())
	logger.Warn("saving failed", "error", errors.New("disk full"), "file", "a b")
	logger.Error("stopping", "reason", "a\tb")
	logger.Info("started") // below warning: not written
	logger.With("file", "5-000000000001.chunk").WithGroup("sync").
		Error("failed", "ms", 120, slog.Group("disk", "free", 0), slog.Group("", "inline", true))
	logger.WithGroup("unused").Warn("no attributes")
	must(t, logs.Close())

	data, err := os.ReadFile(filepath.Join(dir, "error-2026-11-13.log"))
	must(t, err)
	eq(t, string(data), "08:36:36\tWARN\tsaving failed\terror=disk full\tfile=a b\n"+
		"08:36:36\tERROR\tstopping\treason=a\\tb\n"+
		"08:36:36\tERROR\tfailed\tfile=5-000000000001.chunk\tsync.ms=120\tsync.disk.free=0\tsync.inline=true\n"+
		"08:36:36\tWARN\tno attributes\n")
}

// TestNilLogsDoNothing: with logging off, callers use the logs OpenLogs
// returns without asking whether logging is on, and a nil *Logs takes the
// same calls: nothing panics, fails or is written, and the handler drops
// every record.
func TestNilLogsDoNothing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs")
	off, err := OpenLogs(LogConfig{Enabled: false, Dir: dir, PlainDays: 7, ArchiveDays: 30}, time.Now)
	must(t, err)
	off.Access.Line("create", "short.example", "aB3xK", "203.0.113.7", "1")
	off.Errors.Line("WARN", "saving failed")
	off.Admin.Line("ok", "account", "list")

	for _, logs := range []*Logs{off, nil} {
		for _, level := range []slog.Level{slog.LevelDebug, slog.LevelInfo, slog.LevelWarn, slog.LevelError, slog.LevelError + 4} {
			eq(t, logs.Handler().Enabled(t.Context(), level), false)
		}
		slog.New(logs.Handler()).Error("dropped")
		must(t, logs.Tidy())
		must(t, logs.Close())
	}
	var day *DayLog
	day.Line("create")
	eq(t, exists(dir, ""), false) // not even the folder
}

// TestOpenLogsRefusesAFolderItCannotWrite: a log folder that cannot be
// written stops the start, instead of every line being dropped later. A
// folder that is not empty, where the check's file goes, stops root too; a
// read-only folder would not.
func TestOpenLogsRefusesAFolderItCannotWrite(t *testing.T) {
	dir := t.TempDir()
	must(t, os.MkdirAll(filepath.Join(dir, ".qisqa-probe", "in the way"), 0o700))
	_, err := OpenLogs(LogConfig{Enabled: true, Dir: dir, PlainDays: 7, ArchiveDays: 30}, time.Now)
	wantErr(t, err, dir)

	// The check leaves nothing behind in a folder it can write.
	dir = t.TempDir()
	_, err = OpenLogs(LogConfig{Enabled: true, Dir: dir, PlainDays: 7, ArchiveDays: 30}, time.Now)
	must(t, err)
	entries, err := os.ReadDir(dir)
	must(t, err)
	eq(t, len(entries), 0)
}

// TestOpenLogsFollowsNoLink: a link planted at the name of the check's file
// in a log folder others can write does not lead the check to empty the
// file it points at.
func TestOpenLogsFollowsNoLink(t *testing.T) {
	dir := t.TempDir()
	target := writeFile(t, t.TempDir(), "meta.json", "kept\n")
	must(t, os.Symlink(target, filepath.Join(dir, ".qisqa-probe")))

	OpenLogs(LogConfig{Enabled: true, Dir: dir, PlainDays: 7, ArchiveDays: 30}, time.Now) // either outcome
	data, err := os.ReadFile(target)
	must(t, err)
	eq(t, string(data), "kept\n")
}

// TestLogFollowsNoLink: a link planted at the name of a day's file gets no
// lines written to the file it points at. That is a write error like any
// other: reported once, and no request fails.
func TestLogFollowsNoLink(t *testing.T) {
	reported := captureLog(t)
	dir := t.TempDir()
	target := writeFile(t, t.TempDir(), "meta.json", "kept\n")
	logs := openLogs(t, dir, &testClock{now: logTime})
	must(t, os.Symlink(target, filepath.Join(dir, "access-2026-11-13.log")))

	logs.Access.Line("create", "short.example", "aB3xK", "203.0.113.7", "1")
	logs.Access.Line("delete", "short.example", "aB3xK", "203.0.113.7", "1")
	data, err := os.ReadFile(target)
	must(t, err)
	eq(t, string(data), "kept\n")
	eq(t, strings.Count(reported.String(), "access log is failing"), 1)
}

// TestLogSurvivesWriteError: logging never fails a request. A failure is
// reported once, and logging resumes by itself when it can.
func TestLogSurvivesWriteError(t *testing.T) {
	reported := captureLog(t)
	dir := filepath.Join(t.TempDir(), "logs")
	clock := &testClock{now: logTime}
	logs := openLogs(t, dir, clock)
	logs.Access.Line("create")

	// Replace the folder with a plain file: the next day's file cannot be
	// created.
	must(t, os.RemoveAll(dir))
	must(t, os.WriteFile(dir, []byte("in the way"), 0o600))
	clock.now = logTime.AddDate(0, 0, 1)
	logs.Access.Line("lost") // must not panic
	logs.Access.Line("lost")
	eq(t, strings.Count(reported.String(), "access log is failing"), 1)

	must(t, os.Remove(dir))
	logs.Access.Line("delete")
	must(t, logs.Close())
	eq(t, strings.Join(logLines(t, dir, "access-2026-11-14.log"), "\n"), "08:36:36\tdelete")
}

// TestErrorLogFailureDoesNotHang: when the program's own log goes to the
// error log, a failing error log reports its failure through that same log
// and must not wait for itself.
func TestErrorLogFailureDoesNotHang(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs")
	// Not closed at the end: if logging hangs, closing would hang too.
	logs, err := OpenLogs(LogConfig{Enabled: true, Dir: dir, PlainDays: 7, ArchiveDays: 30}, (&testClock{now: logTime}).Now)
	must(t, err)
	old := slog.Default()
	slog.SetDefault(slog.New(logs.Handler()))
	t.Cleanup(func() { slog.SetDefault(old) })
	must(t, os.RemoveAll(dir))
	must(t, os.WriteFile(dir, []byte("in the way"), 0o600))

	done := make(chan struct{})
	go func() {
		slog.Error("first")
		slog.Error("second")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("logging to a failing error log hangs")
	}
}

func TestTidy(t *testing.T) {
	dir := t.TempDir()
	clock := &testClock{now: t0}
	name := func(daysAgo int) string { return "access-" + t0.AddDate(0, 0, -daysAgo).Format(dayLayout) + ".log" }
	for daysAgo := range 8 {
		writeFile(t, dir, name(daysAgo), fmt.Sprintf("line of day %d\n", daysAgo))
	}
	writeFile(t, dir, "notes.txt", "not a log")
	writeFile(t, dir, name(3)+".gz.tmp", "half written")
	// Not compressed in this run: removed only as a left-over.
	writeFile(t, dir, name(1)+".gz.tmp", "half written")
	// Names that only look like a log's.
	for _, other := range []string{"other-2026-10-01.log", "access-2026-10.log", "access-2026-10-01.txt"} {
		writeFile(t, dir, other, "not a log")
	}

	logs, err := OpenLogs(LogConfig{Enabled: true, Dir: dir, PlainDays: 2, ArchiveDays: 3}, clock.Now)
	must(t, err)
	must(t, logs.Tidy())
	for daysAgo := range 8 {
		text, archive := exists(dir, name(daysAgo)), exists(dir, name(daysAgo)+".gz")
		switch {
		case daysAgo < 2:
			eq(t, text, true)
			eq(t, archive, false)
		case daysAgo < 5:
			eq(t, text, false)
			eq(t, archive, true)
		default:
			eq(t, text || archive, false)
		}
	}
	eq(t, exists(dir, "notes.txt"), true)
	eq(t, exists(dir, name(3)+".gz.tmp"), false)
	eq(t, exists(dir, name(1)+".gz.tmp"), false)
	for _, other := range []string{"other-2026-10-01.log", "access-2026-10.log", "access-2026-10-01.txt"} {
		eq(t, exists(dir, other), true)
	}
	eq(t, gunzip(t, filepath.Join(dir, name(3)+".gz")), "line of day 3\n")

	// An archive is removed when its day passes too.
	clock.now = t0.AddDate(0, 0, 1)
	must(t, logs.Tidy())
	eq(t, exists(dir, name(4)+".gz"), false)
	eq(t, exists(dir, name(3)+".gz"), true)
}

// TestTidyWithoutArchive: with archive_days 0, a day is removed when it
// stops being kept as text, and an archive made before is removed too.
func TestTidyWithoutArchive(t *testing.T) {
	dir := t.TempDir()
	name := func(daysAgo int) string { return "error-" + t0.AddDate(0, 0, -daysAgo).Format(dayLayout) + ".log" }
	for daysAgo := range 5 {
		writeFile(t, dir, name(daysAgo), "line\n")
	}
	writeFile(t, dir, name(3)+".gz", "made before")

	logs, err := OpenLogs(LogConfig{Enabled: true, Dir: dir, PlainDays: 2, ArchiveDays: 0}, (&testClock{now: t0}).Now)
	must(t, err)
	must(t, logs.Tidy())
	entries, err := os.ReadDir(dir)
	must(t, err)
	var left []string
	for _, entry := range entries {
		left = append(left, entry.Name())
	}
	eq(t, strings.Join(left, " "), name(1)+" "+name(0))
}

// TestTidyNeverCompressesToday: whatever the days, the file being written
// today stays as it is.
func TestTidyNeverCompressesToday(t *testing.T) {
	dir := t.TempDir()
	clock := &testClock{now: logTime}
	logs, err := OpenLogs(LogConfig{Enabled: true, Dir: dir, PlainDays: 0, ArchiveDays: 0}, clock.Now)
	must(t, err)
	logs.Admin.Line("ok", "domain", "list")
	must(t, logs.Tidy())
	logs.Admin.Line("ok", "account", "list")
	must(t, logs.Close())
	eq(t, len(logLines(t, dir, "admin-2026-11-13.log")), 2)
}

// TestTidyGoesOnAfterAnError: a file that cannot be compressed is named in
// the error, and every other file is still tidied.
func TestTidyGoesOnAfterAnError(t *testing.T) {
	dir := t.TempDir()
	day := func(daysAgo int) string { return t0.AddDate(0, 0, -daysAgo).Format(dayLayout) }
	// The stuck file is listed first: access sorts before error.
	stuck, compressed, removed := "access-"+day(3)+".log", "error-"+day(3)+".log", "error-"+day(9)+".log"
	writeFile(t, dir, stuck, "stuck\n")
	writeFile(t, dir, compressed, "compressed\n")
	writeFile(t, dir, removed, "removed\n")
	// A folder where the archive is written: it can be neither written nor
	// removed, whoever runs the test.
	must(t, os.MkdirAll(filepath.Join(dir, stuck+".gz.tmp", "in the way"), 0o700))

	logs, err := OpenLogs(LogConfig{Enabled: true, Dir: dir, PlainDays: 2, ArchiveDays: 3}, (&testClock{now: t0}).Now)
	must(t, err)
	wantErr(t, logs.Tidy(), stuck)
	eq(t, exists(dir, stuck), true)
	eq(t, gunzip(t, filepath.Join(dir, compressed+".gz")), "compressed\n")
	eq(t, exists(dir, compressed), false)
	eq(t, exists(dir, removed), false)
}
