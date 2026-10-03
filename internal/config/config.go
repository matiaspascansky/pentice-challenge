package config

import (
	"fmt"
	"os"
	"time"
)

// Config agrupa la configuración del servidor. Se lee de env vars con defaults
// razonables para correr en local sin setear nada.
type Config struct {
	Port            string
	GuestWebhookURL string
	DataFile        string
	WorkerTick      time.Duration
	BackoffBase     time.Duration
	BackoffMax      time.Duration
	NotifyTimeout   time.Duration
}

// Load arma la config desde el entorno. Devuelve error si una variable está
// seteada pero con un valor inválido (preferimos fallar al arrancar).
func Load() (Config, error) {
	c := Config{
		Port:            env("PORT", "3000"),
		GuestWebhookURL: env("GUEST_WEBHOOK_URL", "http://localhost:4000/notifications"),
		DataFile:        env("DATA_FILE", "./data/store.json"),
	}

	var err error
	if c.WorkerTick, err = envDuration("WORKER_TICK", 500*time.Millisecond); err != nil {
		return Config{}, err
	}
	if c.BackoffBase, err = envDuration("BACKOFF_BASE", time.Second); err != nil {
		return Config{}, err
	}
	if c.BackoffMax, err = envDuration("BACKOFF_MAX", 30*time.Second); err != nil {
		return Config{}, err
	}
	if c.NotifyTimeout, err = envDuration("NOTIFY_TIMEOUT", 2*time.Second); err != nil {
		return Config{}, err
	}
	return c, nil
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envDuration(key string, def time.Duration) (time.Duration, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return def, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return d, nil
}
