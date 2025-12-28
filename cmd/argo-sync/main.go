package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/iamhalje/argo-sync/internal/buildinfo"
	"github.com/iamhalje/argo-sync/internal/tui"
)

func main() {
	var (
		configPath   string
		contextsCSV  string
		parallel     int
		waitTimeout  time.Duration
		pollInterval time.Duration
		noWait       bool
		debug        bool
		logFile      string
		printVer     bool
	)

	flag.StringVar(&configPath, "config", "", "path to argocd config (default: ~/.config/argocd/config)")
	flag.StringVar(&contextsCSV, "contexts", "", "comma-separated context allowlist Default: all contexts")
	flag.IntVar(&parallel, "parallel", 20, "parallelism limit for targets")
	flag.DurationVar(&waitTimeout, "wait-timeout", 10*time.Minute, "sync wait timeout per target")
	flag.DurationVar(&pollInterval, "poll-interval", 2*time.Second, "poll interval while waiting for sync")
	flag.BoolVar(&noWait, "no-wait", false, "do not wait for sync to finish")
	flag.BoolVar(&debug, "debug", false, "enable debug logging")
	flag.StringVar(&logFile, "log-file", "", "write logs to file (use with --debug)")
	flag.BoolVar(&printVer, "version", false, "print version info and exit")
	flag.Parse()

	if printVer {
		fmt.Println("argo-sync", buildinfo.Short())
		return
	}

	if configPath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			configPath = filepath.FromSlash(".config/argocd/config")
		} else {
			configPath = filepath.Join(home, filepath.FromSlash(".config/argocd/config"))
		}
	}

	opts := tui.Options{
		ConfigPath:   configPath,
		Contexts:     parseContexts(contextsCSV),
		Parallel:     parallel,
		WaitTimeout:  waitTimeout,
		PollInterval: pollInterval,
		NoWait:       noWait,
	}

	level := slog.LevelInfo
	if debug {
		level = slog.LevelDebug
	}

	w := io.Writer(os.Stderr)
	var closeLogFile func()
	if strings.TrimSpace(logFile) != "" {
		f, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to open --log-file %q: %v\n", logFile, err)
			os.Exit(2)
		}
		closeLogFile = func() { _ = f.Close() }
		w = io.Writer(f)
	}
	if closeLogFile != nil {
		defer closeLogFile()
	}

	logger := slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{
		Level: level,
	}))
	logger.Debug(
		"starting argo-sync",
		slog.String("config", opts.ConfigPath),
		slog.Any("contexts", opts.Contexts),
		slog.Int("parallel", opts.Parallel),
		slog.Duration("wait_timeout", opts.WaitTimeout),
		slog.Duration("poll_interval", opts.PollInterval),
		slog.Bool("no_wait", opts.NoWait),
	)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if err := tui.Run(ctx, logger, opts); err != nil {
		logger.Error("argo-sync exited with error", slog.Any("err", err))
		os.Exit(1)
	}
}

func parseContexts(csv string) []string {
	csv = strings.TrimSpace(csv)
	if csv == "" {
		return nil
	}
	parts := strings.Split(csv, ",")
	out := make([]string, 0, len(parts))
	seen := map[string]struct{}{}
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	return out
}
