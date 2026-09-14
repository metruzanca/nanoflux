package config

import (
	"os"
	"time"
)

type Config struct {
	Addr          string
	DBPath        string
	PollInterval  time.Duration
	BootstrapUser string
	BootstrapPass string
}

func Load() Config {
	return Config{
		Addr:          getenv("RSS_ADDR", ":8080"),
		DBPath:        getenv("RSS_DB", "./data/rss.db"),
		PollInterval:  durationEnv("RSS_POLL_INTERVAL", 15*time.Minute),
		BootstrapUser: os.Getenv("RSS_BOOTSTRAP_USER"),
		BootstrapPass: os.Getenv("RSS_BOOTSTRAP_PASS"),
	}
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func durationEnv(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}
