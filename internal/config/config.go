package config

import (
	"os"
	"path/filepath"
	"strconv"
	"time"
)

type Config struct {
	Addr          string
	DBPath        string
	FileStoreDir  string
	LogLevel      string
	PollInterval  time.Duration
	PollWorkers   int
	BootstrapUser string
	BootstrapPass string
}

func Load() Config {
	dbPath := getenv("NF_DB", "./data/rss.db")
	return Config{
		Addr:          getenv("NF_ADDR", ":8080"),
		DBPath:        dbPath,
		FileStoreDir:  getenv("NF_FILE_STORE", filepath.Join(filepath.Dir(dbPath), "filestore")),
		LogLevel:      getenv("NF_LOG_LEVEL", "info"),
		PollInterval:  durationEnv("NF_POLL_INTERVAL", 15*time.Minute),
		PollWorkers:   intEnv("NF_POLL_WORKERS", 4),
		BootstrapUser: os.Getenv("NF_ADMIN_USER"),
		BootstrapPass: os.Getenv("NF_ADMIN_PASS"),
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
