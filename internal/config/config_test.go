package config_test

import (
	"testing"
	"time"

	"pentice-challenge/internal/config"
)

func TestLoadDefaults(t *testing.T) {
	// t.Setenv con "" no alcanza: Load trata el vacío como ausente, que es
	// justo lo que queremos verificar.
	for _, key := range []string{"PORT", "GUEST_WEBHOOK_URL", "DATA_FILE", "WORKER_TICK", "BACKOFF_BASE", "BACKOFF_MAX", "NOTIFY_TIMEOUT"} {
		t.Setenv(key, "")
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	want := config.Config{
		Port:            "3000",
		GuestWebhookURL: "http://localhost:4000/notifications",
		DataFile:        "./data/store.json",
		WorkerTick:      500 * time.Millisecond,
		BackoffBase:     time.Second,
		BackoffMax:      30 * time.Second,
		NotifyTimeout:   2 * time.Second,
	}
	if cfg != want {
		t.Errorf("Load() = %+v, want %+v", cfg, want)
	}
}

func TestLoadFromEnvironment(t *testing.T) {
	t.Setenv("PORT", "9000")
	t.Setenv("GUEST_WEBHOOK_URL", "http://device:1234/notify")
	t.Setenv("DATA_FILE", "/tmp/store.json")
	t.Setenv("WORKER_TICK", "100ms")
	t.Setenv("BACKOFF_BASE", "2s")
	t.Setenv("BACKOFF_MAX", "1m")
	t.Setenv("NOTIFY_TIMEOUT", "5s")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	want := config.Config{
		Port:            "9000",
		GuestWebhookURL: "http://device:1234/notify",
		DataFile:        "/tmp/store.json",
		WorkerTick:      100 * time.Millisecond,
		BackoffBase:     2 * time.Second,
		BackoffMax:      time.Minute,
		NotifyTimeout:   5 * time.Second,
	}
	if cfg != want {
		t.Errorf("Load() = %+v, want %+v", cfg, want)
	}
}

// Preferimos fallar al arrancar antes que correr con una duración silenciosa.
func TestLoadRejectsInvalidDurations(t *testing.T) {
	for _, key := range []string{"WORKER_TICK", "BACKOFF_BASE", "BACKOFF_MAX", "NOTIFY_TIMEOUT"} {
		t.Run(key, func(t *testing.T) {
			t.Setenv(key, "nope")
			if _, err := config.Load(); err == nil {
				t.Errorf("Load() error = nil, want a failure for %s", key)
			}
		})
	}
}
