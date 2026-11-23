// Package config reads service configuration from environment variables.
package config

import (
	"fmt"
	"strconv"
)

// Config holds the service configuration. More fields arrive in later phases.
type Config struct {
	Port int
}

// Load builds a Config using getenv (normally os.Getenv). It fails on
// invalid values so the process can stop at startup.
func Load(getenv func(string) string) (Config, error) {
	cfg := Config{Port: 8080}

	if v := getenv("PORT"); v != "" {
		port, err := strconv.Atoi(v)
		if err != nil || port < 1 || port > 65535 {
			return Config{}, fmt.Errorf("PORT must be an integer between 1 and 65535, got %q", v)
		}
		cfg.Port = port
	}
	return cfg, nil
}
