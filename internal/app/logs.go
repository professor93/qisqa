// Logs: the access, error and admin logs, each kept in one text file per UTC
// day with one tab-separated line per event. Old days are compressed and
// then removed by the program itself, so no external log rotation is needed.
package app

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	dayLayout  = "2006-01-02" // in file names
	timeLayout = "15:04:05"   // at the start of each line
)

// logNames are the names the three logs give their files.
var logNames = []string{"access", "error", "admin"}

// probeName is the file OpenLogs creates and removes to check that the log
// folder can be written. Tidy leaves it alone, like any name not a log's.
const probeName = ".qisqa-probe"

// Logs are the three logs of the log folder. With logging off, OpenLogs
// returns Logs whose three logs are nil, so that callers never ask whether
// logging is on: every call on them does nothing. A nil *Logs takes the
// same method calls.
type Logs struct {
	Access *DayLog // who created, edited, deleted or opened which link
	Errors *DayLog // the program's warnings and errors
	Admin  *DayLog // the admin commands and their results

	cfg     LogConfig
	now     func() time.Time
	tidying sync.Mutex // one Tidy at a time: two would write the same .tmp
}

// OpenLogs creates the log folder, for the owner alone: the access log holds
// the addresses of clients and the codes of secured links. A folder that
// cannot be written is an error, rather than every line dropped later. now
// is the clock. A log creates its file at its first line of the day.
func OpenLogs(cfg LogConfig, now func() time.Time) (*Logs, error) {
	if !cfg.Enabled {
		return &Logs{}, nil
	}
	if err := writable(cfg.Dir); err != nil {
		return nil, fmt.Errorf("log.dir: %w", err)
	}
	day := func(name string) *DayLog { return &DayLog{dir: cfg.Dir, name: name, now: now} }
	return &Logs{Access: day("access"), Errors: day("error"), Admin: day("admin"), cfg: cfg, now: now}, nil
}

// writable creates dir if needed and checks that a file can be created in
// it. The check leaves no file behind. Whatever is at the check's name is
// removed first and the file is created anew, so that a link planted there
// is never followed.
func writable(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	probe := filepath.Join(dir, probeName)
	if err := remove(probe); err != nil {
		return err
	}
	f, err := os.OpenFile(probe, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	f.Close()
	return remove(probe)
}

// on reports whether l writes anything: not when it is nil or logging is
// off.
func (l *Logs) on() bool {
	return l != nil && l.cfg.Enabled
}

// Close closes the files the logs have open.
func (l *Logs) Close() error {
	if !l.on() {
		return nil
	}
	return errors.Join(l.Access.close(), l.Errors.close(), l.Admin.close())
}

// DayLog is one log kept in one file per UTC day, <dir>/<name>-<date>.log.
// A nil *DayLog does nothing.
type DayLog struct {
	dir, name string
	now       func() time.Time

	mu      sync.Mutex
	day     string   // UTC date of the open file
	file    *os.File // nil before the first line and after a failure
	failing bool     // a failure was reported and nothing written since
	line    []byte   // reused for every line
}

// Line writes one line: the time, then each field after a tab. Every field
// is escaped, so a line stays one line whatever its fields hold. Logging
// never fails a request: a failure is reported once, and writing is tried
// again at the next line.
func (l *DayLog) Line(fields ...string) {
	if l == nil {
		return
	}
	if err := l.write(fields); err != nil {
		// Outside the lock: the report may come back to this very log.
		slog.Error(l.name+" log is failing, lines are being dropped", "error", err)
	}
}

// write writes one line. It returns the error to report: only the first
// failure after a line that was written.
func (l *DayLog) write(fields []string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now().UTC()
	err := l.open(now.Format(dayLayout))
	if err == nil {
		b := now.AppendFormat(l.line[:0], timeLayout)
		for _, field := range fields {
			b = append(b, '\t')
			b = append(b, escapeField(field)...)
		}
		l.line = append(b, '\n')
		_, err = l.file.Write(l.line)
	}
	switch {
	case err == nil:
		l.failing = false
	case !l.failing:
		l.failing = true
		return err
	}
	return nil
}

// open makes the file of day the open one, creating the folder and the file
// if they do not exist. A link at the file's name is not followed: it is an
// error, so no line lands in the file it points at. The caller holds the
// lock.
func (l *DayLog) open(day string) error {
	if l.file != nil && l.day == day {
		return nil
	}
	if l.file != nil {
		l.file.Close()
		l.file = nil
	}
	if err := os.MkdirAll(l.dir, 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(filepath.Join(l.dir, l.name+"-"+day+".log"),
		os.O_APPEND|os.O_CREATE|os.O_WRONLY|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	l.file, l.day = file, day
	return nil
}

// close closes the open file. A later line opens it again.
func (l *DayLog) close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	return err
}

// escapeField writes a backslash, tab, newline and carriage return as \\,
// \t, \n and \r, and every other control byte as \x and two hex digits, so
// that a tab only ever separates fields. Other text is kept as it is. A
// field with nothing to escape is returned without a copy.
func escapeField(s string) string {
	i := 0
	for i < len(s) && s[i] >= 0x20 && s[i] != 0x7f && s[i] != '\\' {
		i++
	}
	if i == len(s) {
		return s
	}
	const hex = "0123456789abcdef"
	b := make([]byte, i, len(s)+8)
	copy(b, s)
	for ; i < len(s); i++ {
		switch c := s[i]; {
		case c == '\\':
			b = append(b, `\\`...)
		case c == '\t':
			b = append(b, `\t`...)
		case c == '\n':
			b = append(b, `\n`...)
		case c == '\r':
			b = append(b, `\r`...)
		case c < 0x20 || c == 0x7f:
			b = append(b, '\\', 'x', hex[c>>4], hex[c&0xf])
		default:
			b = append(b, c)
		}
	}
	return string(b)
}

// Handler returns a slog handler that writes warnings and errors to the
// error log: the level, the message, then one key=value field per
// attribute. The time is the log's clock, as on every other line. With
// logging off the handler discards every record.
func (l *Logs) Handler() slog.Handler {
	if !l.on() {
		return slog.DiscardHandler
	}
	return &errorHandler{log: l.Errors}
}

// errorHandler is the slog handler of the error log.
type errorHandler struct {
	log    *DayLog
	fields []string // key=value of the attributes given to WithAttrs
	prefix string   // the groups given to WithGroup, each followed by "."
}

func (h *errorHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= slog.LevelWarn
}

func (h *errorHandler) Handle(_ context.Context, r slog.Record) error {
	fields := make([]string, 0, 2+len(h.fields)+r.NumAttrs())
	fields = append(fields, r.Level.String(), r.Message)
	fields = append(fields, h.fields...)
	r.Attrs(func(a slog.Attr) bool {
		fields = appendAttr(fields, h.prefix, a)
		return true
	})
	h.log.Line(fields...)
	return nil
}

func (h *errorHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	fields := slices.Clip(h.fields) // appending copies: h keeps its own
	for _, a := range attrs {
		fields = appendAttr(fields, h.prefix, a)
	}
	return &errorHandler{log: h.log, fields: fields, prefix: h.prefix}
}

func (h *errorHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	return &errorHandler{log: h.log, fields: h.fields, prefix: h.prefix + name + "."}
}

// appendAttr appends a as a key=value field, the attributes of a group
// each under the group's name. An empty attribute is left out.
func appendAttr(fields []string, prefix string, a slog.Attr) []string {
	a.Value = a.Value.Resolve()
	switch {
	case a.Equal(slog.Attr{}):
		return fields
	case a.Value.Kind() == slog.KindGroup:
		if a.Key != "" {
			prefix += a.Key + "."
		}
		for _, member := range a.Value.Group() {
			fields = appendAttr(fields, prefix, member)
		}
		return fields
	}
	return append(fields, prefix+a.Key+"="+a.Value.String())
}

// Tidy compresses and removes old days. A file's day is read from its name,
// never from the file's time. Younger than plain_days days, a file stays as
// text; then, for archive_days days, it is kept compressed as <name>.gz;
// then it is removed, text or archive. Today's file is never compressed: a
// log writes only to the file of the current day. Files with other names
// are left alone, and a .tmp an interrupted Tidy left behind is removed.
// An error on one file does not stop the others; all are returned at the
// end.
func (l *Logs) Tidy() error {
	if !l.on() {
		return nil
	}
	l.tidying.Lock()
	defer l.tidying.Unlock()
	entries, err := os.ReadDir(l.cfg.Dir)
	if err != nil {
		return err
	}
	today := l.now().UTC().Truncate(24 * time.Hour) // midnight: a UTC day has 24 hours
	var errs []error
	for _, entry := range entries {
		if err := l.tidy(entry.Name(), today); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// tidy keeps, compresses or removes one file of the log folder.
func (l *Logs) tidy(name string, today time.Time) error {
	day, rest, ok := parseLogName(name)
	if !ok {
		return nil
	}
	path := filepath.Join(l.cfg.Dir, name)
	age := int(today.Sub(day) / (24 * time.Hour))
	plain := max(l.cfg.PlainDays, 1) // today's file is always kept as it is
	switch {
	case rest == ".gz.tmp":
		return remove(path)
	case age < plain:
		return nil
	case age-plain < l.cfg.ArchiveDays: // not age < plain+archive: no overflow
		if rest == "" {
			return compress(path)
		}
		return nil
	default:
		return remove(path)
	}
}

// parseLogName reads the day of a log file's name, such as
// access-2026-11-14.log.gz, and what follows ".log": "", ".gz" or
// ".gz.tmp". ok is false for any other name.
func parseLogName(name string) (day time.Time, rest string, ok bool) {
	log, after, _ := strings.Cut(name, "-")
	date, rest, found := strings.Cut(after, ".log")
	if !slices.Contains(logNames, log) || !found || (rest != "" && rest != ".gz" && rest != ".gz.tmp") {
		return time.Time{}, "", false
	}
	day, err := time.Parse(dayLayout, date)
	return day, rest, err == nil
}

// compress writes the file at path to path.gz with gzip's best compression,
// streaming it, and removes path once the archive is safely in place: it is
// written to path.gz.tmp, synced and renamed, and the folder is synced.
func compress(path string) error {
	in, err := os.Open(path)
	if err != nil {
		return err
	}
	defer in.Close()
	// A left-over .tmp goes first: the archive must be created by us,
	// private, and not take over another file's mode.
	tmp := path + ".gz.tmp"
	if err := remove(tmp); err != nil {
		return err
	}
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	zw, _ := gzip.NewWriterLevel(out, gzip.BestCompression) // a valid level: no error
	_, err = io.Copy(zw, in)
	if err == nil {
		err = zw.Close()
	}
	if err == nil {
		err = out.Sync()
	}
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(tmp, path+".gz")
	}
	if err == nil {
		err = syncDir(filepath.Dir(path), (*os.File).Sync)
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	return remove(path)
}

// remove removes a file that may be gone already.
func remove(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
