// Package config provides configuration management for the Sub-Translate CLI tool.
// It supports loading from CLI flags, JSON config file, and defaults.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"time"
)

// Config holds all configuration for the application.
type Config struct {
	InputDir       string        `json:"input_dir"`       // Directory to scan for .srt files
	APIUrl         string        `json:"api_url"`         // LibreTranslate API base URL
	BatchSize      int           `json:"batch_size"`      // Number of subtitle lines per API request
	MaxConcurrency int           `json:"max_concurrency"` // Number of parallel file workers
	MaxRetries     int           `json:"max_retries"`     // Max retries per API request
	RateLimit      float64       `json:"rate_limit"`      // API requests per second
	RequestDelay   time.Duration `json:"request_delay"`   // Delay between API requests (CPU throttle)
	Timeout        time.Duration `json:"timeout"`         // Timeout per file translation
	BatchTimeout   time.Duration `json:"batch_timeout"`   // Timeout per batch translation attempt
	DryRun         bool          `json:"dry_run"`         // Scan only, don't translate
	Overwrite      bool          `json:"overwrite"`       // Overwrite existing .vi.srt files
	Verbose        bool          `json:"verbose"`         // Debug-level logging

	// Persistence
	CacheFile string `json:"cache_file"` // Path to persistent translation cache file
	StateFile string `json:"state_file"` // Path to job resume state file
	Resume    bool   `json:"resume"`     // If true, skip files already completed in StateFile

	// Cache TTL: how long a translated entry stays valid (0 = never expire)
	CacheTTL time.Duration `json:"cache_ttl"`

	// Adaptive throttling
	Adaptive bool `json:"adaptive"` // Enable adaptive delay/batch throttling
}

// Default returns a Config with sensible defaults optimized for stable CPU usage.
func Default() *Config {
	return &Config{
		InputDir:       ".",
		APIUrl:         "http://localhost:5000",
		BatchSize:      25,
		MaxConcurrency: 3,
		MaxRetries:     3,
		RateLimit:      5,
		RequestDelay:   200 * time.Millisecond,
		Timeout:        5 * time.Minute,
		BatchTimeout:   3 * time.Minute,
		DryRun:         false,
		Overwrite:      false,
		Verbose:        false,
		CacheFile:      ".subtranslate_cache.json",
		StateFile:      ".subtranslate_state.json",
		Resume:         false,
	}
}

// LoadFromFile reads a JSON config file and merges non-zero values into cfg.
func LoadFromFile(path string, cfg *Config) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading config file: %w", err)
	}

	// Decode into a temporary struct to detect which fields were set.
	var fileCfg Config
	if err := json.Unmarshal(data, &fileCfg); err != nil {
		return fmt.Errorf("parsing config file: %w", err)
	}

	// Merge: only override if the file value is non-zero.
	if fileCfg.InputDir != "" {
		cfg.InputDir = fileCfg.InputDir
	}
	if fileCfg.APIUrl != "" {
		cfg.APIUrl = fileCfg.APIUrl
	}
	if fileCfg.BatchSize > 0 {
		cfg.BatchSize = fileCfg.BatchSize
	}
	if fileCfg.MaxConcurrency > 0 {
		cfg.MaxConcurrency = fileCfg.MaxConcurrency
	}
	if fileCfg.MaxRetries > 0 {
		cfg.MaxRetries = fileCfg.MaxRetries
	}
	if fileCfg.RateLimit > 0 {
		cfg.RateLimit = fileCfg.RateLimit
	}
	if fileCfg.RequestDelay > 0 {
		cfg.RequestDelay = fileCfg.RequestDelay
	}
	if fileCfg.Timeout > 0 {
		cfg.Timeout = fileCfg.Timeout
	}
	if fileCfg.BatchTimeout > 0 {
		cfg.BatchTimeout = fileCfg.BatchTimeout
	}
	if fileCfg.DryRun {
		cfg.DryRun = true
	}
	if fileCfg.Overwrite {
		cfg.Overwrite = true
	}
	if fileCfg.Verbose {
		cfg.Verbose = true
	}
	if fileCfg.Resume {
		cfg.Resume = true
	}
	if fileCfg.CacheFile != "" {
		cfg.CacheFile = fileCfg.CacheFile
	}
	if fileCfg.StateFile != "" {
		cfg.StateFile = fileCfg.StateFile
	}
	if fileCfg.CacheTTL > 0 {
		cfg.CacheTTL = fileCfg.CacheTTL
	}

	return nil
}

// Validate checks that the config values are valid.
func (c *Config) Validate() error {
	// InputDir must exist and be a directory.
	info, err := os.Stat(c.InputDir)
	if err != nil {
		return fmt.Errorf("input directory %q: %w", c.InputDir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("input path %q is not a directory", c.InputDir)
	}

	// APIUrl must be a valid URL.
	u, err := url.Parse(c.APIUrl)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("invalid API URL %q", c.APIUrl)
	}

	// Numeric constraints.
	var errs []error
	if c.BatchSize <= 0 {
		errs = append(errs, fmt.Errorf("batch_size must be > 0, got %d", c.BatchSize))
	}
	if c.MaxConcurrency <= 0 {
		errs = append(errs, fmt.Errorf("max_concurrency must be > 0, got %d", c.MaxConcurrency))
	}
	if c.MaxRetries < 0 {
		errs = append(errs, fmt.Errorf("max_retries must be >= 0, got %d", c.MaxRetries))
	}
	if c.RateLimit <= 0 {
		errs = append(errs, fmt.Errorf("rate_limit must be > 0, got %f", c.RateLimit))
	}
	if c.RequestDelay < 0 {
		errs = append(errs, fmt.Errorf("request_delay must be >= 0, got %s", c.RequestDelay))
	}
	if c.Timeout <= 0 {
		errs = append(errs, fmt.Errorf("timeout must be > 0, got %s", c.Timeout))
	}
	if c.BatchTimeout <= 0 {
		errs = append(errs, fmt.Errorf("batch_timeout must be > 0, got %s", c.BatchTimeout))
	}

	return errors.Join(errs...)
}
