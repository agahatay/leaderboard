package config

import "testing"

func TestLoad(t *testing.T) {
	tests := []struct {
		name     string
		env      map[string]string
		wantPort int
		wantErr  bool
	}{
		{"defaults", map[string]string{}, 8080, false},
		{"custom port", map[string]string{"PORT": "9090"}, 9090, false},
		{"port not a number", map[string]string{"PORT": "abc"}, 0, true},
		{"port zero", map[string]string{"PORT": "0"}, 0, true},
		{"port too large", map[string]string{"PORT": "70000"}, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			getenv := func(key string) string { return tt.env[key] }
			cfg, err := Load(getenv)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if cfg.Port != tt.wantPort {
				t.Errorf("Port = %d, want %d", cfg.Port, tt.wantPort)
			}
		})
	}
}
