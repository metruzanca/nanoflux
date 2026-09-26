package config

import (
	"os"
	"path/filepath"
	"strconv"
	"time"
)

type Config struct {
	Addr         string
	DBPath       string
	FileStoreDir string
	LogLevel     string
	PollInterval time.Duration
	PollWorkers  int
	// PollHostSpacing is the default minimum spacing between two fetches to the
	// same registrable host, applied even before that host ever rate-limits.
	// Zero disables it; a learned rate-limit window still overrides it.
	PollHostSpacing time.Duration
	BootstrapUser   string
	BootstrapPass   string
	PluginsDir      string
	Backup          BackupConfig
}

// BackupConfig configures automatic instance backups. It is disabled unless a
// destination (Dir or S3) and an interval are set.
type BackupConfig struct {
	Interval time.Duration // NF_BACKUP_INTERVAL; 0 disables automatic backups
	Keep     int           // NF_BACKUP_KEEP; <= 0 keeps every snapshot
	Dir      string        // NF_BACKUP_DIR: local destination directory
	S3       BackupS3
}

// BackupS3 is the S3-compatible destination for backups. It is deliberately
// separate from the app's NF_S3_* blob config so backups can target a different
// bucket or account.
type BackupS3 struct {
	Endpoint  string
	Bucket    string
	AccessKey string
	SecretKey string
	Region    string
	Prefix    string
}

// Enabled reports whether automatic backups should run.
func (b BackupConfig) Enabled() bool {
	return b.Interval > 0 && (b.Dir != "" || b.S3.Endpoint != "")
}

// UsesS3 reports whether the destination is S3-compatible (rather than local).
func (b BackupConfig) UsesS3() bool { return b.S3.Endpoint != "" }

func Load() Config {
	dbPath := getenv("NF_DB", "./data/rss.db")
	return Config{
		Addr:         getenv("NF_ADDR", ":8080"),
		DBPath:       dbPath,
		FileStoreDir: getenv("NF_FILE_STORE", filepath.Join(filepath.Dir(dbPath), "filestore")),
		LogLevel:     getenv("NF_LOG_LEVEL", "info"),
		PollInterval: durationEnv("NF_POLL_INTERVAL", 15*time.Minute),
		PollWorkers:  intEnv("NF_POLL_WORKERS", 4),
		// 30s matches the poller's wake floor; 0 disables the default spacing.
		PollHostSpacing: durationEnv("NF_POLL_HOST_SPACING", 30*time.Second),
		BootstrapUser:   os.Getenv("NF_ADMIN_USER"),
		BootstrapPass:   os.Getenv("NF_ADMIN_PASS"),
		PluginsDir:      getenv("NF_PLUGINS_DIR", "./plugins"),
		Backup: BackupConfig{
			Interval: durationEnv("NF_BACKUP_INTERVAL", 0),
			Keep:     intEnv("NF_BACKUP_KEEP", 7),
			Dir:      os.Getenv("NF_BACKUP_DIR"),
			S3: BackupS3{
				Endpoint:  os.Getenv("NF_BACKUP_S3_ENDPOINT"),
				Bucket:    getenv("NF_BACKUP_S3_BUCKET", "nanoflux-backups"),
				AccessKey: os.Getenv("NF_BACKUP_S3_ACCESS_KEY"),
				SecretKey: os.Getenv("NF_BACKUP_S3_SECRET_KEY"),
				Region:    getenv("NF_BACKUP_S3_REGION", "us-east-1"),
				Prefix:    os.Getenv("NF_BACKUP_S3_PREFIX"),
			},
		},
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
