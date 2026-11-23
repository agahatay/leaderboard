// Package config reads service configuration from environment variables.
package config

import (
	"errors"
	"fmt"
	"strconv"
	"time"
)

// Config holds the service configuration. More fields arrive in later phases.
type Config struct {
	Port          int
	RedisAddr     string
	RedisPassword string
	RedisTimeout  time.Duration
}

// Load builds a Config using getenv (normally os.Getenv). Every invalid value
// is reported at once so the process can stop at startup.
func Load(getenv func(string) string) (Config, error) {
	cfg := Config{
		Port:          8080,
		RedisAddr:     "localhost:6379",
		RedisPassword: getenv("REDIS_PASSWORD"),
		RedisTimeout:  200 * time.Millisecond,
	}
	var errs []error

	if v := getenv("PORT"); v != "" {
		port, err := strconv.Atoi(v)
		if err != nil || port < 1 || port > 65535 {
			errs = append(errs, fmt.Errorf("PORT must be an integer between 1 and 65535, got %q", v))
		}
		cfg.Port = port
	}
	if v := getenv("REDIS_ADDR"); v != "" {
		cfg.RedisAddr = v
	}
	if v := getenv("REDIS_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			errs = append(errs, fmt.Errorf("REDIS_TIMEOUT must be a positive duration like 200ms, got %q", v))
		}
		cfg.RedisTimeout = d
	}

	if err := errors.Join(errs...); err != nil {
		return Config{}, err
	}
	return cfg, nil
}
