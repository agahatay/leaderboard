package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoad(t *testing.T) {
	defaults := Config{Port: 8080, RedisAddr: "localhost:6379", RedisTimeout: 200 * time.Millisecond}

	tests := []struct {
		name    string
		env     map[string]string
		want    Config
		wantErr []string // substrings that must all appear in the error
	}{
		{"defaults", map[string]string{}, defaults, nil},
		{"custom values", map[string]string{
			"PORT": "9090", "REDIS_ADDR": "redis:6379", "REDIS_PASSWORD": "s3cret", "REDIS_TIMEOUT": "1s",
		}, Config{Port: 9090, RedisAddr: "redis:6379", RedisPassword: "s3cret", RedisTimeout: time.Second}, nil},
		{"port not a number", map[string]string{"PORT": "abc"}, Config{}, []string{"PORT"}},
		{"port zero", map[string]string{"PORT": "0"}, Config{}, []string{"PORT"}},
		{"port too large", map[string]string{"PORT": "70000"}, Config{}, []string{"PORT"}},
		{"timeout without unit", map[string]string{"REDIS_TIMEOUT": "200"}, Config{}, []string{"REDIS_TIMEOUT"}},
		{"timeout negative", map[string]string{"REDIS_TIMEOUT": "-1s"}, Config{}, []string{"REDIS_TIMEOUT"}},
		{"all errors reported", map[string]string{"PORT": "x", "REDIS_TIMEOUT": "y"}, Config{}, []string{"PORT", "REDIS_TIMEOUT"}},
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
