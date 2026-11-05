package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Port            int      `yaml:"port"`
	ComicDirs       []string `yaml:"comic_dirs"`
	CacheMaxItems   int      `yaml:"cache_max_items"`
	DBPath          string   `yaml:"db_path"`
	CoverDir        string   `yaml:"cover_dir"`
	BubblesDir      string   `yaml:"bubbles_dir,omitempty"`
	ScanInterval    string   `yaml:"scan_interval,omitempty"`
	CBRCacheMaxGB   float64  `yaml:"cbr_cache_max_gb,omitempty"`
	ComicVineAPIKey string   `yaml:"comicvine_api_key,omitempty"`
	LocalCVDBPath   string   `yaml:"localcv_db_path,omitempty"`
	// GCDDBPath is the offline Grand Comics Database dump, read-only. Unset
	// disables the GCD fallback pass and GCD results in the console search.
	GCDDBPath       string `yaml:"gcd_db_path,omitempty"`
	ScanFileDelayMs int    `yaml:"scan_file_delay_ms,omitempty"`
	MylarURL        string `yaml:"mylar_url,omitempty"`
	MetronUser      string `yaml:"metron_user,omitempty"`
	MetronPass      string `yaml:"metron_pass,omitempty"`

	TaggerEnabled       bool   `yaml:"tagger_enabled,omitempty"`
	TaggerQuietHours    string `yaml:"tagger_quiet_hours,omitempty"`
	TaggerPauseBetweenS int    `yaml:"tagger_pause_between_s,omitempty"`
	TaggerMinPages      int    `yaml:"tagger_min_pages,omitempty"`

	// ponytail: one flag, not a general read-only mode. The writeback worker is
	// the only file mutator that starts on its own; organize and the tagger both
	// need a deliberate trigger. Set on the native test instance so it can never
	// write into the real library while production holds the same files.
	WritebackDisabled bool `yaml:"writeback_disabled,omitempty"`

	// AutoPipeline runs match → confirm → enrich → writeback → organize on
	// the series each scheduled scan found new or changed. Off by default:
	// scans still record pending work, so enabling it later catches up.
	AutoPipeline bool `yaml:"auto_pipeline,omitempty"`

	// Push enables native iOS notifications. Absent (the default, and on
	// dev/staging) means push is off everywhere.
	Push PushConfig `yaml:"push,omitempty"`

	// AdminPassword turns on HTTP Basic auth for /_admin (any username).
	// Empty turns it off: the console is open, as it was before.
	AdminPassword string `yaml:"admin_password,omitempty"`

	mu   sync.RWMutex
	path string
}

// PushConfig is the APNs token-auth key plus the shared secret the app sends
// to register its device token.
type PushConfig struct {
	KeyPath        string `yaml:"key_path"` // relative paths resolve against the config file's dir
	KeyID          string `yaml:"key_id"`
	TeamID         string `yaml:"team_id"`
	BundleID       string `yaml:"bundle_id"`
	RegisterSecret string `yaml:"register_secret"`
}

// PushEnabled reports whether every push field is set.
func (c *Config) PushEnabled() bool {
	return c.PushKeyConfigured() && c.Push.RegisterSecret != ""
}

// PushKeyConfigured reports whether there's enough to send pushes. The host
// watcher checks this, not PushEnabled: it never registers devices, and the
// secret may live only in the container's PUSH_REGISTER_SECRET.
func (c *Config) PushKeyConfigured() bool {
	p := c.Push
	return p.KeyPath != "" && p.KeyID != "" && p.TeamID != "" && p.BundleID != ""
}

// Dir is the directory holding the config file.
func (c *Config) Dir() string { return filepath.Dir(c.path) }

// PushKeyPath resolves key_path against Dir, so the same config works inside
// the container and from the host watcher.
func (c *Config) PushKeyPath() string {
	if filepath.IsAbs(c.Push.KeyPath) {
		return c.Push.KeyPath
	}
	return filepath.Join(c.Dir(), c.Push.KeyPath)
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}
	cfg := &Config{
		Port:                8080,
		CacheMaxItems:       500,
		DBPath:              "comic-reader.db",
		CBRCacheMaxGB:       2,
		TaggerPauseBetweenS: 30,
		TaggerMinPages:      60,
		path:                path,
	}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}
	// YAML can't tell a missing key from an empty one, so "off" is the
	// explicit manual-only value and empty means the default.
	if cfg.ScanInterval == "" {
		cfg.ScanInterval = "30m"
	}
	if cfg.CoverDir == "" {
		cfg.CoverDir = filepath.Join(filepath.Dir(cfg.DBPath), "covers")
	}
	if len(cfg.ComicDirs) == 0 {
		return nil, fmt.Errorf("comic_dirs must not be empty")
	}
	if key := os.Getenv("COMICVINE_API_KEY"); key != "" {
		cfg.ComicVineAPIKey = key
	}
	if v := os.Getenv("MYLAR_URL"); v != "" {
		cfg.MylarURL = v
	}
	if v := os.Getenv("METRON_USER"); v != "" {
		cfg.MetronUser = v
	}
	if v := os.Getenv("METRON_PASS"); v != "" {
		cfg.MetronPass = v
	}
	if v := os.Getenv("PUSH_REGISTER_SECRET"); v != "" {
		cfg.Push.RegisterSecret = v
	}
	return cfg, nil
}

func (c *Config) Save() error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	// Marshal the Config itself so a new field can't be forgotten here
	// (bubbles_dir was); mu and path are unexported and skipped.
	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("marshaling config: %w", err)
	}
	return os.WriteFile(c.path, data, 0644)
}

func (c *Config) AddDir(dir string) error {
	c.mu.Lock()
	for _, d := range c.ComicDirs {
		if d == dir {
			c.mu.Unlock()
			return fmt.Errorf("directory already exists")
		}
	}
	c.ComicDirs = append(c.ComicDirs, dir)
	c.mu.Unlock()
	return c.Save()
}

func (c *Config) RemoveDir(dir string) error {
	c.mu.Lock()
	if len(c.ComicDirs) <= 1 {
		c.mu.Unlock()
		return fmt.Errorf("cannot remove last directory")
	}
	filtered := make([]string, 0, len(c.ComicDirs))
	for _, d := range c.ComicDirs {
		if d != dir {
			filtered = append(filtered, d)
		}
	}
	c.ComicDirs = filtered
	c.mu.Unlock()
	return c.Save()
}

func (c *Config) SetScanInterval(interval string) error {
	c.mu.Lock()
	c.ScanInterval = interval
	c.mu.Unlock()
	return c.Save()
}

func (c *Config) Path() string {
	return c.path
}

type ConfigSnapshot struct {
	Port          int
	ComicDirs     []string
	CacheMaxItems int
	DBPath        string
	CoverDir      string
	ScanInterval  string
	CBRCacheMaxGB float64
}

func (c *Config) Snapshot() ConfigSnapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()
	dirs := make([]string, len(c.ComicDirs))
	copy(dirs, c.ComicDirs)
	return ConfigSnapshot{
		Port: c.Port, ComicDirs: dirs, CacheMaxItems: c.CacheMaxItems,
		DBPath: c.DBPath, CoverDir: c.CoverDir, ScanInterval: c.ScanInterval,
		CBRCacheMaxGB: c.CBRCacheMaxGB,
	}
}
