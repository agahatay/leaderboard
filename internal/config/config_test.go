package config

import (
	"maps"
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

func TestRedisURL(t *testing.T) {
	base := map[string]string{"API_KEY": "k", "REDIS_ADDR": "ignored:6379", "REDIS_PASSWORD": "ignored"}

	tests := []struct {
		name         string
		url          string
		wantAddr     string
		wantPassword string
		wantTLS      bool
		wantDB       int
	}{
		{"not set uses REDIS_ADDR", "", "ignored:6379", "ignored", false, 0},
		{"redis url", "redis://red-abc123:6379", "red-abc123:6379", "", false, 0},
		{"with user, password and db", "redis://user:pw@cache.example:6380/2", "cache.example:6380", "pw", false, 2},
		{"tls", "rediss://default:pw@cache.example:6379", "cache.example:6379", "pw", true, 0},
		{"default port", "redis://cache.example", "cache.example:6379", "", false, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := maps.Clone(base)
			env["REDIS_URL"] = tt.url
			cfg, err := Load(func(k string) string { return env[k] })
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			opts, err := cfg.RedisOptions()
			if err != nil {
				t.Fatalf("RedisOptions: %v", err)
			}
			if opts.Addr != tt.wantAddr || opts.Password != tt.wantPassword || opts.DB != tt.wantDB {
				t.Errorf("addr=%q password=%q db=%d, want %q %q %d",
					opts.Addr, opts.Password, opts.DB, tt.wantAddr, tt.wantPassword, tt.wantDB)
			}
			if gotTLS := opts.TLSConfig != nil; gotTLS != tt.wantTLS {
				t.Errorf("TLS = %v, want %v", gotTLS, tt.wantTLS)
			}
		})
	}
}

// TestRedisURLInvalid checks that a bad URL stops startup and that the error,
// which is logged, never contains the URL or its password.
func TestRedisURLInvalid(t *testing.T) {
	const secret = "hunter2-secret"
	for _, url := range []string{
		"http://user:" + secret + "@cache.example:6379",  // wrong scheme
		"redis://user:" + secret + "@cache.example:port", // bad port
		"redis://user:" + secret + "@cache.example/notanumber",
		"redis://user:" + secret + "%zz@cache.example", // bad escape, url.Parse fails
	} {
		t.Run(url, func(t *testing.T) {
			env := map[string]string{"API_KEY": "k", "REDIS_URL": url}
			_, err := Load(func(k string) string { return env[k] })
			if err == nil {
				t.Fatal("want an error for an invalid REDIS_URL")
			}
			if !strings.Contains(err.Error(), "REDIS_URL") {
				t.Errorf("error %q does not name REDIS_URL", err)
			}
			if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "cache.example") {
				t.Errorf("error leaks the URL: %q", err)
			}
		})
	}
}
