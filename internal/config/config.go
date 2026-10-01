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
	PluginsDir      string
	Backup          BackupConfig
	Demo            DemoConfig
	Analytics       AnalyticsConfig
}

// AnalyticsConfig adds an optional, privacy-first site analytics tracker
// (Umami) to every rendered page. It is off by default: NF_ANALYTICS_ENABLED
// must be set on, and both the script URL and website id must be present.
type AnalyticsConfig struct {
	On        bool   // NF_ANALYTICS_ENABLED: explicit opt-in switch
	ScriptURL string // NF_ANALYTICS_SCRIPT_URL: tracker script URL
	WebsiteID string // NF_ANALYTICS_WEBSITE_ID: website id in the dashboard
	HostURL   string // NF_ANALYTICS_HOST_URL: optional data endpoint override
	Tag       string // NF_ANALYTICS_TAG: optional tag to group events
}

// Enabled reports whether analytics should run. The explicit switch and both
// required values are needed, so a half-configured instance stays off.
func (a AnalyticsConfig) Enabled() bool {
	return a.On && a.ScriptURL != "" && a.WebsiteID != ""
}

// DemoConfig turns on ephemeral demo sessions for public "marketing" deployments.
// NF_DEMO_MODE shows the landing page CTA and enables POST /demo; the other
// fields tune how a demo account is provisioned.
type DemoConfig struct {
	Mode     bool          // NF_DEMO_MODE: enable ephemeral demo sessions
	User     string        // NF_DEMO_USER: username of the admin seed user to clone
	TTL      time.Duration // NF_DEMO_TTL: lifetime of an ephemeral session
	MaxFeeds int           // NF_DEMO_MAX_FEEDS: extra feeds a demo user may add
}

// Enabled reports whether demo mode is on and a seed user is configured.
func (d DemoConfig) Enabled() bool { return d.Mode && d.User != "" }

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
		// 60s matches reddit's anonymous per-IP window (the tightest host we
		// know of); 0 disables the default spacing.
		PollHostSpacing: durationEnv("NF_POLL_HOST_SPACING", 60*time.Second),
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
		Demo: DemoConfig{
			Mode:     boolEnv("NF_DEMO_MODE", false),
			User:     os.Getenv("NF_DEMO_USER"),
			TTL:      durationEnv("NF_DEMO_TTL", 2*time.Hour),
			MaxFeeds: intEnv("NF_DEMO_MAX_FEEDS", 5),
		},
		Analytics: AnalyticsConfig{
			On:        boolEnv("NF_ANALYTICS_ENABLED", false),
			ScriptURL: os.Getenv("NF_ANALYTICS_SCRIPT_URL"),
			WebsiteID: os.Getenv("NF_ANALYTICS_WEBSITE_ID"),
			HostURL:   os.Getenv("NF_ANALYTICS_HOST_URL"),
			Tag:       os.Getenv("NF_ANALYTICS_TAG"),
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

// boolEnv parses a boolean env var. An unset or unparseable value yields def.
func boolEnv(key string, def bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}
