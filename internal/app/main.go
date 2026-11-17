// qisqa is a URL shortener: one binary that keeps its links in its own data
// folder.
//
// This file is the entry point: command-line flags, the order in which the
// parts start, the background jobs, a clean shutdown, and the command line
// of the admin commands.
package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// version is the program's version, which Main is given. The main.go at the
// root of the repository has it set when a release is built:
//
//	go build -ldflags "-X main.version=1.2.3"
var version = "dev"

// How often the background jobs run, and how long shutdown may take.
const (
	reloadEvery  = 5 * time.Second  // check the config file for changes
	sweepEvery   = time.Minute      // forget clients the limiter no longer needs
	removedEvery = 10 * time.Second // let go of chunk files removed from the data folder
	tidyEvery    = time.Hour        // compress and remove old log files

	shutdownTimeout = 10 * time.Second // finishing the requests in progress
)

// openStore is OpenStore. Tests replace it to reach the store of a program
// they run.
var openStore = OpenStore

// Main is the program as the command starts it: it runs run with the
// command-line arguments after the program's name, stops on Ctrl-C or a TERM
// signal, and returns the exit status.
func Main(programVersion string, args []string, stdout, stderr io.Writer) int {
	version = programVersion
	ctx, stop := signalContext()
	code := run(ctx, args, stdout, stderr)
	stop()
	return code
}

// signalContext returns a context that ends when the program is asked to
// stop: Ctrl-C or a TERM signal. From then on those signals have their usual
// effect again, so a second Ctrl-C ends a shutdown that takes too long.
func signalContext() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-signals
		signal.Stop(signals) // first, so nobody sees ctx end while signals are still caught
		cancel()
	}()
	return ctx, cancel
}

// run is the whole program. Without command words after the flags it
// serves until ctx ends and returns the exit code: 0 after a clean shutdown,
// 1 when the program could not start or keep serving, 2 for wrong
// command-line flags. With command words it sends them to the running
// server as an admin command instead: 0 when the command worked, 1 when not.
// With -skip it adds to the skip of a data folder no qisqa runs on, and
// starts nothing: 0 when that worked, 1 when not.
func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("qisqa", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "config.yaml", "path to the configuration file")
	dataDir := flags.String("data", "", "the data folder of the server an admin command, or -skip, is for (default: the config's data_dir)")
	showVersion := flags.Bool("version", false, "print the version and exit")
	var skip skipFlag
	flags.Var(&skip, "skip", "after putting back a copy of the data folder, and before starting qisqa: pass over `N` more numbers of every numbering, so that the codes made since the copy are not handed out again")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *showVersion {
		fmt.Fprintln(stdout, "qisqa", version)
		return 0
	}
	command := flags.Args()
	if skip.set {
		if len(command) > 0 {
			fmt.Fprintf(stderr, "-skip takes no command words: %q\n", command)
			return 2
		}
		return addSkip(*configPath, *dataDir, skip.n, stdout, stderr)
	}
	if len(command) > 0 {
		return sendCommand(*configPath, *dataDir, command, stdout, stderr)
	}
	if *dataDir != "" {
		fmt.Fprintln(stderr, "-data names the folder of an admin command or of -skip; the server takes data_dir from its config")
		return 2
	}

	toStderr := slog.NewTextHandler(stderr, nil)
	slog.SetDefault(slog.New(toStderr))
	if err := serve(ctx, *configPath, toStderr); err != nil {
		slog.Error("qisqa failed", "error", err)
		return 1
	}
	slog.Info("qisqa stopped")
	return 0
}

// sendCommand sends an admin command to the server that uses the data
// folder dataDir, else the config's data_dir, prints the answer and returns
// the exit code. It opens nothing in the folder but the server's socket.
func sendCommand(configPath, dataDir string, command []string, stdout, stderr io.Writer) int {
	if dataDir == "" {
		cfg, err := LoadConfig(configPath)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		dataDir = cfg.DataDir
	}
	output, err := SendAdmin(dataDir, command)
	if err != nil {
		fmt.Fprintln(stderr, strings.TrimRight(err.Error(), "\n"))
		return 1
	}
	if output != "" {
		fmt.Fprintln(stdout, strings.TrimRight(output, "\n"))
	}
	return 0
}

// skipFlag is the value of -skip: a whole number of at least 1. set says
// that the flag was given, so that -skip 0 is not taken for no -skip.
type skipFlag struct {
	n   uint64
	set bool
}

func (f *skipFlag) String() string { return strconv.FormatUint(f.n, 10) }

func (f *skipFlag) Set(s string) error {
	n, err := strconv.ParseUint(s, 10, 64)
	switch {
	case err != nil:
		return errors.New("not a whole number")
	case n == 0:
		return errors.New("passing over 0 numbers does nothing")
	}
	f.n, f.set = n, true
	return nil
}

// addSkip adds n to the skip of the data folder dataDir, else the config's
// data_dir, prints the new total and returns the exit code. The folder must
// be there and hold a key, since it is the copy put back, and is looked at
// and opened as the server would: no qisqa may run on it. Nothing else is
// started.
func addSkip(configPath, dataDir string, n uint64, stdout, stderr io.Writer) int {
	if dataDir == "" {
		cfg, err := LoadConfig(configPath)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		dataDir = cfg.DataDir
	}
	if _, err := os.Stat(dataDir); errors.Is(err, fs.ErrNotExist) {
		fmt.Fprintf(stderr, "data folder %s does not exist: put the copy back there first\n", dataDir)
		return 1
	}
	group, err := checkFolder(dataDir)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if group {
		fmt.Fprintf(stderr, "the folder's group may write to it; run chmod g-w on it unless that is meant: %s\n", dataDir)
	}
	// A copy put back has its key. A folder without one, such as a new
	// volume or the data folder of another working folder, is not the copy,
	// and nothing is written into it.
	if _, err := os.Stat(filepath.Join(dataDir, "key")); errors.Is(err, fs.ErrNotExist) {
		fmt.Fprintf(stderr, "data folder %s has no key, so it is not a copy of a data folder: put the copy back there, or name the folder it is in with -config or -data\n", dataDir)
		return 1
	}
	store, err := openStore(dataDir)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	total, err := store.Skip(n)
	if closeErr := store.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "added %d to %s, which now holds %d: when qisqa starts, every numbering goes on %d numbers further than it would have\n",
		n, filepath.Join(dataDir, "skipped"), total, n)
	return 0
}

// serve starts every part, serves requests until ctx ends or a sync in the
// data folder fails, and then shuts down in order: stop taking requests,
// finish the ones in progress, stop the background jobs, close the admin
// socket, the data folder and, last, the logs, so that what closing the
// data folder logs reaches the error log too. After a failed sync, the last
// one that closing the data folder makes included, it returns that sync's
// error. The program's own log goes to toStderr.
func serve(ctx context.Context, configPath string, toStderr slog.Handler) (serveErr error) {
	cfg, err := LoadConfig(configPath)
	if err != nil {
		return err
	}
	// Before anything is written into the folders. A warning waits for the
	// logs, so that it reaches the error log too.
	dirs := []string{cfg.DataDir}
	if cfg.Log.Enabled {
		dirs = append(dirs, cfg.Log.Dir)
	}
	var groupWritable []string
	for _, dir := range dirs {
		group, err := checkFolder(dir)
		if err != nil {
			return err
		}
		if group {
			groupWritable = append(groupWritable, dir)
		}
	}

	// The store comes first: its lock makes a second qisqa on the folder
	// fail here, and it creates the data folder the meta file is in.
	store, err := openStore(cfg.DataDir)
	if err != nil {
		return err
	}
	// Its error is serve's when serve has none of its own: a stop in order
	// whose last sync failed ends with status 1.
	closeStore := func() {
		if err := store.Close(); err != nil {
			slog.Error("closing the data folder failed", "error", err)
			if serveErr == nil {
				serveErr = err
			}
		}
	}
	// For when serve ends before the logs are open; later it finds the folder
	// closed already.
	defer closeStore()
	store.syncEvery = cfg.SyncEvery.D() // before the store is first used, as fatal
	// A failed sync, the disk itself failing, ends the program by the same
	// shutdown as a signal, so that whatever supervises qisqa starts it again
	// from what is on disk. The store's syncing goroutine calls fatal, also
	// while Close waits for it: fatal only passes the error on, and never
	// waits.
	diskFailed := make(chan error, 1)
	store.fatal = func(err error) {
		select {
		case diskFailed <- err:
		default: // the store calls it once; a second call would add nothing
		}
	}
	meta, err := OpenMeta(filepath.Join(cfg.DataDir, "meta.json"))
	if err != nil {
		return err
	}

	logs, err := OpenLogs(cfg.Log, time.Now) // does nothing when the log is off
	if err != nil {
		return err
	}
	defer func() {
		if err := logs.Close(); err != nil {
			slog.Error("closing the logs failed", "error", err)
		}
	}()
	// Warnings and errors go to the error log too, until the logs are closed.
	slog.SetDefault(slog.New(slog.NewMultiHandler(toStderr, logs.Handler())))
	defer slog.SetDefault(slog.New(toStderr))
	defer closeStore() // before the logs close, so that a failed last sync reaches the error log
	for _, dir := range groupWritable {
		slog.Warn("the folder's group may write to it; run chmod g-w on it unless that is meant", "folder", dir)
	}

	watcher := NewWatcher(configPath, cfg)
	server := NewServer(watcher, meta, store, logs)

	// The admin commands reach this server, and its Meta, through the socket.
	admin, err := ListenAdmin(cfg.DataDir, server)
	if err != nil {
		return err
	}
	defer func() {
		if err := admin.Close(); err != nil {
			slog.Error("closing the admin socket failed", "error", err)
		}
	}()

	// Listening before the jobs start makes a busy port a startup error.
	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return err
	}
	httpServer := &http.Server{
		Handler: server.Handler(),
		// Limits that keep slow or stuck connections from piling up.
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	jobsCtx, stopJobs := context.WithCancel(ctx)
	defer stopJobs()
	var jobs sync.WaitGroup
	jobs.Go(func() { watcher.Run(jobsCtx, reloadEvery) })
	jobs.Go(func() { every(jobsCtx, sweepEvery, server.SweepLimiter) })
	jobs.Go(func() { every(jobsCtx, removedEvery, server.ForgetRemovedFiles) })
	tidy := func() {
		if err := logs.Tidy(); err != nil {
			slog.Error("tidying the log folder failed", "error", err)
		}
	}
	jobs.Go(func() {
		tidy() // at startup too, beside the start rather than before it
		every(jobsCtx, tidyEvery, tidy)
	})

	slog.Info("qisqa started", "version", version, "listen", listener.Addr().String(),
		"config", configPath, "data_dir", cfg.DataDir, "admin", admin.Addr().String())
	failed := make(chan error, 1)
	go func() { failed <- httpServer.Serve(listener) }()

	select {
	case serveErr = <-failed:
	case serveErr = <-diskFailed:
		slog.Info("shutting down after the failed sync")
	case <-ctx.Done():
		slog.Info("shutting down")
	}

	// ctx may have ended, so the shutdown gets a context of its own.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		slog.Warn("some requests did not finish in time", "error", err)
	}
	stopJobs()
	jobs.Wait()
	return serveErr
}

// checkFolder looks at the mode of a folder qisqa keeps files in, before
// anything is written into it. A folder every user of the machine may write
// to is an error: anyone could put a file of their own there, or take one
// of qisqa's away. groupCanWrite reports one its group may write to. A
// folder that is not there yet passes, for qisqa creates it for its own
// user alone, and so does one that cannot be looked at: opening it then
// says what is wrong. A link to the folder is followed.
func checkFolder(dir string) (groupCanWrite bool, err error) {
	info, err := os.Stat(dir)
	if err != nil {
		return false, nil
	}
	perm := info.Mode().Perm()
	if perm&0o002 != 0 {
		return false, fmt.Errorf("every user of this machine may write to the folder %s (mode %04o): run chmod o-w %s", dir, perm, dir)
	}
	return perm&0o020 != 0, nil
}

// every calls job once per interval until ctx ends.
func every(ctx context.Context, interval time.Duration, job func()) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			job()
		}
	}
}
