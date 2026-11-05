package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadAndSave(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	os.WriteFile(path, []byte("port: 9090\ncomic_dirs:\n  - /comics\n"), 0644)

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Port != 9090 {
		t.Errorf("port = %d, want 9090", cfg.Port)
	}

	cfg.ScanInterval = "6h"
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}

	cfg2, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg2.ScanInterval != "6h" {
		t.Errorf("scan_interval = %q, want 6h", cfg2.ScanInterval)
	}
}

func TestScanFileDelayParses(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	os.WriteFile(path, []byte("comic_dirs:\n  - /tmp/comics\nscan_file_delay_ms: 25\n"), 0644)

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ScanFileDelayMs != 25 {
		t.Errorf("ScanFileDelayMs = %d, want 25", cfg.ScanFileDelayMs)
	}
}

func TestAddRemoveDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	os.WriteFile(path, []byte("port: 8080\ncomic_dirs:\n  - /comics\n"), 0644)

	cfg, _ := Load(path)

	if err := cfg.AddDir("/more"); err != nil {
		t.Fatal(err)
	}
	if len(cfg.ComicDirs) != 2 {
		t.Fatalf("dirs = %d, want 2", len(cfg.ComicDirs))
	}

	if err := cfg.AddDir("/more"); err == nil {
		t.Error("expected error adding duplicate dir")
	}

	if err := cfg.RemoveDir("/comics"); err != nil {
		t.Fatal(err)
	}
	if len(cfg.ComicDirs) != 1 {
		t.Fatalf("dirs = %d, want 1", len(cfg.ComicDirs))
	}

	if err := cfg.RemoveDir("/more"); err == nil {
		t.Error("expected error removing last dir")
	}
}

func TestSaveRoundTripsLocalCVDBPath(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/config.yaml"
	if err := os.WriteFile(path, []byte("port: 8080\ncomic_dirs: [/tmp]\ndb_path: x.db\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.LocalCVDBPath = "/data/localcv.db"
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}

	reloaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.LocalCVDBPath != "/data/localcv.db" {
		t.Fatalf("localcv_db_path did not survive Save(): got %q", reloaded.LocalCVDBPath)
	}
}

func TestLoadTaggerConfig(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/c.yaml"
	os.WriteFile(path, []byte("comic_dirs: [\"/x\"]\ntagger_enabled: true\ntagger_min_pages: 80\n"), 0o644)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.TaggerEnabled {
		t.Error("tagger_enabled not parsed")
	}
	if cfg.TaggerMinPages != 80 {
		t.Errorf("min_pages = %d", cfg.TaggerMinPages)
	}
	if cfg.TaggerPauseBetweenS != 30 {
		t.Errorf("default pause = %d, want 30", cfg.TaggerPauseBetweenS)
	}
}

func TestMetronCredentials(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	yaml := "comic_dirs: [\"/tmp\"]\nmetron_user: \"alice\"\nmetron_pass: \"s3cret\"\n"
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MetronUser != "alice" || cfg.MetronPass != "s3cret" {
		t.Errorf("yaml creds not loaded: %q/%q", cfg.MetronUser, cfg.MetronPass)
	}

	t.Setenv("METRON_USER", "bob")
	t.Setenv("METRON_PASS", "hunter2")
	cfg, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MetronUser != "bob" || cfg.MetronPass != "hunter2" {
		t.Errorf("env override must win: %q/%q", cfg.MetronUser, cfg.MetronPass)
	}
}

func TestWritebackDisabledSurvivesSave(t *testing.T) {
	path := t.TempDir() + "/config.yaml"
	if err := os.WriteFile(path, []byte("port: 8082\ncomic_dirs: [/tmp]\ndb_path: x.db\nwriteback_disabled: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.WritebackDisabled {
		t.Fatal("writeback_disabled: true did not load")
	}
	// An admin action that calls Save() must not silently re-enable writeback on
	// the test instance — that would let it mutate the real library.
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	reloaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reloaded.WritebackDisabled {
		t.Fatal("writeback_disabled dropped by Save()")
	}
}

func TestSaveRoundTripsGCDDBPath(t *testing.T) {
	path := t.TempDir() + "/config.yaml"
	if err := os.WriteFile(path, []byte("port: 8080\ncomic_dirs: [/tmp]\ndb_path: x.db\ngcd_db_path: /data/gcd.db\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GCDDBPath != "/data/gcd.db" {
		t.Fatalf("gcd_db_path did not load: %q", cfg.GCDDBPath)
	}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	reloaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.GCDDBPath != "/data/gcd.db" {
		t.Fatalf("gcd_db_path dropped by Save(): %q", reloaded.GCDDBPath)
	}
}

func writeCfg(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadDefaultsScanIntervalTo30m(t *testing.T) {
	cfg, err := Load(writeCfg(t, "comic_dirs: [/x]\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ScanInterval != "30m" {
		t.Errorf("ScanInterval = %q, want 30m", cfg.ScanInterval)
	}
	if cfg.AutoPipeline {
		t.Error("AutoPipeline must default to false")
	}
}

func TestLoadKeepsExplicitOff(t *testing.T) {
	cfg, err := Load(writeCfg(t, "comic_dirs: [/x]\nscan_interval: \"off\"\nauto_pipeline: true\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ScanInterval != "off" || !cfg.AutoPipeline {
		t.Errorf("got interval=%q auto=%v", cfg.ScanInterval, cfg.AutoPipeline)
	}
}

const pushYAML = `comic_dirs:
  - /comics
push:
  key_path: AuthKey.p8
  key_id: KID
  team_id: TEAM
  bundle_id: com.example.app
  register_secret: s3cret
`

func TestPushBlockSurvivesSave(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	os.WriteFile(path, []byte(pushYAML), 0644)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	cfg2, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	want := PushConfig{KeyPath: "AuthKey.p8", KeyID: "KID", TeamID: "TEAM", BundleID: "com.example.app", RegisterSecret: "s3cret"}
	if cfg2.Push != want {
		t.Fatalf("push = %+v", cfg2.Push)
	}
	if !cfg2.PushEnabled() {
		t.Fatal("PushEnabled false with a full block")
	}
	if got := cfg2.PushKeyPath(); got != filepath.Join(dir, "AuthKey.p8") {
		t.Fatalf("PushKeyPath = %q", got)
	}
	if cfg2.Dir() != dir {
		t.Fatalf("Dir = %q", cfg2.Dir())
	}
}

func TestNoPushBlockDisabled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	os.WriteFile(path, []byte("comic_dirs:\n  - /comics\n"), 0644)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PushEnabled() {
		t.Fatal("PushEnabled true without a block")
	}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if strings.Contains(string(b), "push:") {
		t.Fatalf("saved config has a push key:\n%s", b)
	}
}

func TestPushSecretEnvOverride(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	os.WriteFile(path, []byte(pushYAML), 0644)
	t.Setenv("PUSH_REGISTER_SECRET", "fromenv")
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Push.RegisterSecret != "fromenv" {
		t.Fatalf("secret = %q", cfg.Push.RegisterSecret)
	}
}

func TestPushKeyConfiguredIgnoresSecret(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	os.WriteFile(path, []byte(strings.Replace(pushYAML, "  register_secret: s3cret\n", "", 1)), 0644)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PushEnabled() {
		t.Fatal("PushEnabled true without a secret")
	}
	if !cfg.PushKeyConfigured() {
		t.Fatal("PushKeyConfigured false with key fields set (secret lives in the container env)")
	}
}

func TestSaveRoundTripsBubblesDirAndAdminPassword(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	os.WriteFile(path, []byte("comic_dirs: [/c]\nbubbles_dir: /b\nadmin_password: pw\n"), 0644)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	got, _ := Load(path)
	if got.BubblesDir != "/b" || got.AdminPassword != "pw" {
		t.Fatalf("after Save: bubbles_dir=%q admin_password=%q", got.BubblesDir, got.AdminPassword)
	}
}
