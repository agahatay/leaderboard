// Package config reads service configuration from environment variables.
package config

import (
	"errors"
	"fmt"
	"strconv"
	"time"
)

// Ingest modes.
const (
	ModeAsync = "async"
	ModeSync  = "sync"
)

// Config holds the service configuration.
type Config struct {
	Port          int
	RedisAddr     string
	RedisPassword string
	RedisTimeout  time.Duration

	APIKey string

	IngestMode    string
	QueueSize     int
	Workers       int
	BatchSize     int
	FlushInterval time.Duration

	ShutdownTimeout time.Duration
}

// Load builds a Config using getenv (normally os.Getenv). Every invalid value
// is reported at once so the process can stop at startup.
func Load(getenv func(string) string) (Config, error) {
	p := parser{getenv: getenv}
	cfg := Config{
		Port:            p.intIn("PORT", 8080, 1, 65535),
		RedisAddr:       p.str("REDIS_ADDR", "localhost:6379"),
		RedisPassword:   getenv("REDIS_PASSWORD"),
		RedisTimeout:    p.positiveDuration("REDIS_TIMEOUT", 200*time.Millisecond),
		APIKey:          getenv("API_KEY"),
		IngestMode:      p.str("INGEST_MODE", ModeAsync),
		QueueSize:       p.intIn("QUEUE_SIZE", 1024, 1, 1_000_000),
		Workers:         p.intIn("WORKERS", 4, 1, 1024),
		BatchSize:       p.intIn("BATCH_SIZE", 100, 1, 10_000),
		FlushInterval:   p.positiveDuration("FLUSH_INTERVAL", 50*time.Millisecond),
		ShutdownTimeout: p.positiveDuration("SHUTDOWN_TIMEOUT", 10*time.Second),
	}

	if cfg.APIKey == "" {
		p.errs = append(p.errs, errors.New("API_KEY is required"))
	}
	if cfg.IngestMode != ModeAsync && cfg.IngestMode != ModeSync {
		p.errs = append(p.errs, fmt.Errorf("INGEST_MODE must be %q or %q, got %q", ModeAsync, ModeSync, cfg.IngestMode))
	}

	if err := errors.Join(p.errs...); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// parser collects errors so that Load can report all of them together.
type parser struct {
	getenv func(string) string
	errs   []error
}

func (p *parser) str(key, def string) string {
	if v := p.getenv(key); v != "" {
		return v
	}
	return def
}

func (p *parser) intIn(key string, def, lo, hi int) int {
	v := p.getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < lo || n > hi {
		p.errs = append(p.errs, fmt.Errorf("%s must be an integer between %d and %d, got %q", key, lo, hi, v))
	}
	return n
}

func (p *parser) positiveDuration(key string, def time.Duration) time.Duration {
	v := p.getenv(key)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		p.errs = append(p.errs, fmt.Errorf("%s must be a positive duration like 200ms, got %q", key, v))
	}
	return d
}
