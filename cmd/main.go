// Sub-Translate CLI Tool
//
// Automatically finds *_en.srt subtitle files in a directory (recursive),
// translates them from English to Vietnamese using LibreTranslate API,
// and saves the output as *_en.vi.srt.
//
// Usage:
//
//	subtranslate -dir ./movies -api http://localhost:5000
//	subtranslate -dir ./movies --dry-run --verbose
//	subtranslate --config config.json
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"subtranslate/internal/config"
	"subtranslate/internal/orchestrator"
	"subtranslate/internal/report"
	"subtranslate/internal/tui"
)

// Version is set at build time via -ldflags.
var Version = "dev"

func main() {
	os.Exit(run())
}

func run() int {
	// ── CLI Flags ──────────────────────────────────────────────────
	cfg := config.Default()

	flag.StringVar(&cfg.InputDir, "dir", cfg.InputDir, "Directory to scan for *_en.srt files")
	flag.StringVar(&cfg.APIUrl, "api", cfg.APIUrl, "LibreTranslate API URL")
	flag.IntVar(&cfg.BatchSize, "batch", cfg.BatchSize, "Subtitle lines per API request")
	flag.IntVar(&cfg.MaxConcurrency, "concurrency", cfg.MaxConcurrency, "Parallel file workers")
	flag.IntVar(&cfg.MaxRetries, "retries", cfg.MaxRetries, "Max retries per API request")
	flag.Float64Var(&cfg.RateLimit, "rate", cfg.RateLimit, "API requests per second")
	flag.DurationVar(&cfg.RequestDelay, "delay", cfg.RequestDelay, "Delay between API requests (e.g. 200ms)")
	flag.DurationVar(&cfg.Timeout, "timeout", cfg.Timeout, "Timeout per file translation")
	flag.DurationVar(&cfg.BatchTimeout, "batch-timeout", cfg.BatchTimeout, "Timeout per batch translation attempt (default 3m)")
	flag.BoolVar(&cfg.DryRun, "dry-run", cfg.DryRun, "Scan only, don't translate")
	flag.BoolVar(&cfg.Overwrite, "overwrite", cfg.Overwrite, "Overwrite existing .vi.srt files")
	flag.BoolVar(&cfg.Verbose, "verbose", cfg.Verbose, "Enable debug-level logging")
	flag.StringVar(&cfg.CacheFile, "cache", cfg.CacheFile, "Path to persistent translation cache JSON file")
	flag.StringVar(&cfg.StateFile, "state", cfg.StateFile, "Path to job resume state JSON file")
	flag.BoolVar(&cfg.Resume, "resume", cfg.Resume, "Skip files already completed in a previous run (uses --state file)")
	flag.BoolVar(&cfg.Adaptive, "adaptive", cfg.Adaptive, "Enable adaptive delay/batch throttling based on API latency")

	interactive := flag.Bool("interactive", false, "Launch interactive console UI")

	configFile := flag.String("config", "", "Path to JSON config file")
	showVersion := flag.Bool("version", false, "Show version and exit")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Sub-Translate v%s — Translate SRT subtitles EN→VI\n\n", Version)
		fmt.Fprintf(os.Stderr, "Usage:\n  subtranslate [flags]\n\nFlags:\n")
		flag.PrintDefaults()
		fmt.Fprintf(os.Stderr, "\nExamples:\n")
		fmt.Fprintf(os.Stderr, "  subtranslate -dir ./movies -api http://localhost:5000\n")
		fmt.Fprintf(os.Stderr, "  subtranslate -dir ./movies --dry-run --verbose\n")
		fmt.Fprintf(os.Stderr, "  subtranslate --config config.json -dir ./movies\n")
	}

	flag.Parse()

	if *showVersion {
		fmt.Printf("Sub-Translate v%s\n", Version)
		return 0
	}

	// Launch interactive UI if flag is set or no args provided.
	if *interactive || len(os.Args) == 1 {
		logLevel := slog.LevelInfo
		if cfg.Verbose {
			logLevel = slog.LevelDebug
		}
		logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
			Level: logLevel,
		}))

		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()

		app := tui.New(cfg, logger)
		app.Run(ctx)
		return 0
	}

	// ── Config File Loading ───────────────────────────────────────
	// Priority: defaults → config file → CLI flags (flags override file).
	if *configFile != "" {
		// Save flag values, reload defaults, load file, then re-apply flags.
		savedCfg := *cfg
		*cfg = *config.Default()

		if err := config.LoadFromFile(*configFile, cfg); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			return 1
		}

		// Re-apply any CLI flags that were explicitly set.
		flag.Visit(func(f *flag.Flag) {
			switch f.Name {
			case "dir":
				cfg.InputDir = savedCfg.InputDir
			case "api":
				cfg.APIUrl = savedCfg.APIUrl
			case "batch":
				cfg.BatchSize = savedCfg.BatchSize
			case "concurrency":
				cfg.MaxConcurrency = savedCfg.MaxConcurrency
			case "retries":
				cfg.MaxRetries = savedCfg.MaxRetries
			case "rate":
				cfg.RateLimit = savedCfg.RateLimit
			case "delay":
				cfg.RequestDelay = savedCfg.RequestDelay
			case "timeout":
				cfg.Timeout = savedCfg.Timeout
			case "batch-timeout":
				cfg.BatchTimeout = savedCfg.BatchTimeout
			case "dry-run":
				cfg.DryRun = savedCfg.DryRun
			case "overwrite":
				cfg.Overwrite = savedCfg.Overwrite
			case "verbose":
				cfg.Verbose = savedCfg.Verbose
			case "cache":
				cfg.CacheFile = savedCfg.CacheFile
			case "state":
				cfg.StateFile = savedCfg.StateFile
			case "resume":
				cfg.Resume = savedCfg.Resume
			}
		})
	}

	// ── Validation ────────────────────────────────────────────────
	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "Configuration error: %v\n", err)
		return 1
	}

	// ── Logger Setup ──────────────────────────────────────────────
	logLevel := slog.LevelInfo
	if cfg.Verbose {
		logLevel = slog.LevelDebug
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: logLevel,
	}))

	logger.Info("Sub-Translate starting",
		"version", Version,
		"dir", cfg.InputDir,
		"api", cfg.APIUrl,
		"concurrency", cfg.MaxConcurrency,
		"batch_size", cfg.BatchSize,
		"rate_limit", cfg.RateLimit,
		"request_delay", cfg.RequestDelay,
		"dry_run", cfg.DryRun,
	)

	// ── Graceful Shutdown ─────────────────────────────────────────
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// ── Run Orchestrator ──────────────────────────────────────────
	stats, err := orchestrator.Run(ctx, cfg, logger)
	if err != nil {
		logger.Error("orchestrator failed", "error", err)
	}

	// ── Summary Report ────────────────────────────────────────────
	report.Print(os.Stdout, stats)

	if stats.Failed > 0 || err != nil {
		return 1
	}
	return 0
}
