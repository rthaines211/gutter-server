package admin

import (
	"net/http"
	"os"
	"regexp"
	"runtime"
	"time"
)

var secretLineRe = regexp.MustCompile(`(?m)^(\s*[\w-]*(?:_key|_token|_secret|_pass|password)\s*:\s*).+$`)

func redactSecrets(yaml string) string {
	return secretLineRe.ReplaceAllString(yaml, "${1}«redacted»")
}

type ConfigData struct {
	ComicDirs  []DirStatus
	Port       int
	DBPath     string
	CoverDir   string
	CacheItems int
	YamlSource string
	Process    ProcessInfo
	Database   DBInfo
}

type DirStatus struct {
	Path    string
	Mounted bool
}

type ProcessInfo struct {
	Version    string
	GoVersion  string
	PID        int
	StartedAt  string
	Uptime     string
	MemoryMB   int
	Goroutines int
}

type DBInfo struct {
	Driver string
	SizeMB float64
	Tables int
}

func (a *Admin) handleConfig(w http.ResponseWriter, r *http.Request) {
	data := a.baseData("settings")
	data.Title = "Config"

	dirs := make([]DirStatus, 0, len(a.ComicDirs))
	for _, d := range a.ComicDirs {
		mounted := false
		if _, err := os.Stat(d); err == nil {
			mounted = true
		}
		dirs = append(dirs, DirStatus{Path: d, Mounted: mounted})
	}

	_, cacheCap := a.Cache.Stats()

	yamlBytes, _ := os.ReadFile(a.Config.Path())
	yamlSource := redactSecrets(string(yamlBytes))

	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	uptime := time.Since(a.StartedAt).Truncate(time.Second)

	proc := ProcessInfo{
		Version:    "0.4.2",
		GoVersion:  runtime.Version(),
		PID:        os.Getpid(),
		StartedAt:  a.StartedAt.Format("2006-01-02 15:04:05"),
		Uptime:     formatDuration(uptime),
		MemoryMB:   int(mem.Alloc / (1 << 20)),
		Goroutines: runtime.NumGoroutine(),
	}

	dbInfo := DBInfo{Driver: "sqlite3"}
	if info, err := os.Stat(a.DBPath); err == nil {
		dbInfo.SizeMB = float64(info.Size()) / (1 << 20)
	}
	if db := a.Store.DB(); db != nil {
		var count int
		row := db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table'")
		if row.Scan(&count) == nil {
			dbInfo.Tables = count
		}
	}

	data.Content = ConfigData{
		ComicDirs:  dirs,
		Port:       a.Port,
		DBPath:     a.DBPath,
		CoverDir:   a.CoverDir,
		CacheItems: cacheCap,
		YamlSource: yamlSource,
		Process:    proc,
		Database:   dbInfo,
	}

	a.render(w, "config.html", data)
}

func (a *Admin) handleAddDir(w http.ResponseWriter, r *http.Request) {
	dir := r.FormValue("dir")
	if dir == "" {
		http.Error(w, "dir is required", http.StatusBadRequest)
		return
	}
	if err := a.Config.AddDir(dir); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	snap := a.Config.Snapshot()
	a.ComicDirs = snap.ComicDirs
	http.Redirect(w, r, "/_admin/config", http.StatusSeeOther)
}

func (a *Admin) handleRemoveDir(w http.ResponseWriter, r *http.Request) {
	dir := r.FormValue("dir")
	if dir == "" {
		http.Error(w, "dir is required", http.StatusBadRequest)
		return
	}
	if err := a.Config.RemoveDir(dir); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	snap := a.Config.Snapshot()
	a.ComicDirs = snap.ComicDirs
	http.Redirect(w, r, "/_admin/config", http.StatusSeeOther)
}
