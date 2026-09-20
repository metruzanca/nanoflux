package config

import (
	"os"
	"strconv"
	"time"
)

type Config struct {
	Addr          string
	DBPath        string
	LogLevel      string
	PollInterval  time.Duration
	PollWorkers   int
	BootstrapUser string
	BootstrapPass string
}

func Load() Config {
	return Config{
		Addr:          getenv("RSS_ADDR", ":8080"),
		DBPath:        getenv("RSS_DB", "./data/rss.db"),
		LogLevel:      getenv("RSS_LOG_LEVEL", "info"),
		PollInterval:  durationEnv("RSS_POLL_INTERVAL", 15*time.Minute),
		PollWorkers:   intEnv("RSS_POLL_WORKERS", 4),
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

func intEnv(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
