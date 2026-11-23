package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoad(t *testing.T) {
	defaults := Config{
		Port: 8080, RedisAddr: "localhost:6379", RedisTimeout: 200 * time.Millisecond,
		APIKey:     "k",
		IngestMode: ModeAsync, QueueSize: 1024, Workers: 4, BatchSize: 100,
		FlushInterval: 50 * time.Millisecond, ShutdownTimeout: 10 * time.Second,
	}

	// with returns the minimal valid env plus the given overrides.
	with := func(kv ...string) map[string]string {
		env := map[string]string{"API_KEY": "k"}
		for i := 0; i < len(kv); i += 2 {
			env[kv[i]] = kv[i+1]
		}
		return env
	}

	tests := []struct {
		name    string
		env     map[string]string
		want    Config
		wantErr []string // substrings that must all appear in the error
	}{
		{"defaults", with(), defaults, nil},
		{"custom values", with(
			"PORT", "9090", "REDIS_ADDR", "redis:6379", "REDIS_PASSWORD", "s3cret", "REDIS_TIMEOUT", "1s",
			"INGEST_MODE", "sync", "QUEUE_SIZE", "10", "WORKERS", "2", "BATCH_SIZE", "5",
			"FLUSH_INTERVAL", "10ms", "SHUTDOWN_TIMEOUT", "3s",
		), Config{
			Port: 9090, RedisAddr: "redis:6379", RedisPassword: "s3cret", RedisTimeout: time.Second,
			APIKey:     "k",
			IngestMode: ModeSync, QueueSize: 10, Workers: 2, BatchSize: 5,
			FlushInterval: 10 * time.Millisecond, ShutdownTimeout: 3 * time.Second,
		}, nil},
		{"missing API_KEY", map[string]string{}, Config{}, []string{"API_KEY"}},
		{"port not a number", with("PORT", "abc"), Config{}, []string{"PORT"}},
		{"port zero", with("PORT", "0"), Config{}, []string{"PORT"}},
		{"port too large", with("PORT", "70000"), Config{}, []string{"PORT"}},
		{"timeout without unit", with("REDIS_TIMEOUT", "200"), Config{}, []string{"REDIS_TIMEOUT"}},
		{"timeout negative", with("REDIS_TIMEOUT", "-1s"), Config{}, []string{"REDIS_TIMEOUT"}},
		{"unknown ingest mode", with("INGEST_MODE", "fast"), Config{}, []string{"INGEST_MODE"}},
		{"zero workers", with("WORKERS", "0"), Config{}, []string{"WORKERS"}},
		{"zero queue", with("QUEUE_SIZE", "0"), Config{}, []string{"QUEUE_SIZE"}},
		{"zero batch", with("BATCH_SIZE", "0"), Config{}, []string{"BATCH_SIZE"}},
		{"zero flush interval", with("FLUSH_INTERVAL", "0s"), Config{}, []string{"FLUSH_INTERVAL"}},
		{"bad shutdown timeout", with("SHUTDOWN_TIMEOUT", "soon"), Config{}, []string{"SHUTDOWN_TIMEOUT"}},
		{"all errors reported", map[string]string{"PORT": "x", "WORKERS": "y"}, Config{}, []string{"PORT", "WORKERS", "API_KEY"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			getenv := func(key string) string { return tt.env[key] }
			got, err := Load(getenv)
			if len(tt.wantErr) == 0 {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
			} else {
				if err == nil {
					t.Fatal("expected an error")
				}
				for _, s := range tt.wantErr {
					if !strings.Contains(err.Error(), s) {
						t.Errorf("error %q does not mention %s", err, s)
					}
				}
			}
			if got != tt.want {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}
