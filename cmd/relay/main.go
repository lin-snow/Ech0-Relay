// SPDX-License-Identifier: Apache-2.0

// Command relay syncs public Telegram channels into Ech0 instances.
//
// It is designed to run periodically from CI: load config + committed cursor
// state, publish new posts, apply retention, then persist the cursor state
// (which CI commits back). Exit code is non-zero if any sync failed, turning
// the scheduled workflow red.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/lin-snow/Ech0-Relay/internal/config"
	"github.com/lin-snow/Ech0-Relay/internal/ech0"
	"github.com/lin-snow/Ech0-Relay/internal/relay"
	"github.com/lin-snow/Ech0-Relay/internal/state"
	"github.com/lin-snow/Ech0-Relay/internal/telegram"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	var (
		configPath = flag.String("config", "config.yaml", "path to config file")
		statePath  = flag.String("state", "state/state.json", "path to cursor state file")
		dryRun     = flag.Bool("dry-run", false, "scrape and render but do not post, delete, or write state")
		onlySync   = flag.String("sync", "", "run only the sync with this name")
		verbose    = flag.Bool("verbose", false, "debug logging")
		showVer    = flag.Bool("version", false, "print version and exit")
	)
	flag.Parse()

	if *showVer {
		fmt.Println("ech0-relay", version)
		return
	}

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(logger)

	if err := run(logger, *configPath, *statePath, relay.Options{DryRun: *dryRun, OnlySync: *onlySync}); err != nil {
		logger.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger, configPath, statePath string, opts relay.Options) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	st, err := state.Load(statePath)
	if err != nil {
		return err
	}

	deps := relay.Deps{
		Scraper: telegram.NewScraper(),
		NewClient: func(inst config.Instance, token string) relay.EchoClient {
			return ech0.New(inst.BaseURL, token)
		},
		Logger: logger,
	}

	sum := relay.Run(ctx, cfg, st, deps, opts)

	// Persist cursor progress before signalling failure — even a partially
	// failed run has delivered posts whose cursor must stick.
	if !opts.DryRun && st.Dirty() {
		if err := st.Save(statePath); err != nil {
			logger.Error("failed to save state", "err", err, "path", statePath)
			return err
		}
	}

	report := relay.RenderMarkdown(sum)
	fmt.Fprintln(os.Stderr, "\n"+report)
	writeStepSummary(logger, report)

	if sum.HardError {
		return fmt.Errorf("one or more syncs failed")
	}
	logger.Info("run complete", "syncs", len(sum.Results))
	return nil
}

// writeStepSummary appends the markdown report to the GitHub Actions step
// summary when running in CI.
func writeStepSummary(logger *slog.Logger, report string) {
	path := os.Getenv("GITHUB_STEP_SUMMARY")
	if path == "" {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		logger.Warn("cannot open step summary", "err", err)
		return
	}
	defer f.Close()
	if _, err := f.WriteString(report + "\n"); err != nil {
		logger.Warn("cannot write step summary", "err", err)
	}
}
