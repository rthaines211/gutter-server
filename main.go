package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/rthaines211/gutter-server/archive"
	"github.com/rthaines211/gutter-server/arcsync"
	"github.com/rthaines211/gutter-server/cache"
	"github.com/rthaines211/gutter-server/comicvine"
	"github.com/rthaines211/gutter-server/config"
	"github.com/rthaines211/gutter-server/creators"
	"github.com/rthaines211/gutter-server/enrichment"
	"github.com/rthaines211/gutter-server/match"
	"github.com/rthaines211/gutter-server/match/gcd"
	"github.com/rthaines211/gutter-server/match/localcv"
	"github.com/rthaines211/gutter-server/metron"
	"github.com/rthaines211/gutter-server/notify"
	"github.com/rthaines211/gutter-server/push"
	"github.com/rthaines211/gutter-server/server"
	"github.com/rthaines211/gutter-server/server/admin"
	"github.com/rthaines211/gutter-server/store"
	"github.com/rthaines211/gutter-server/tagger"
	"github.com/rthaines211/gutter-server/writeback"
)

func main() {
	configPath := flag.String("config", "config.yaml", "path to config file")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		log.Fatal(err)
	}
	if cfg.AuthOff {
		log.Printf("WARNING: auth is OFF (GUTTER_AUTH=off) — anyone who can reach this port can read and change your library")
	}

	// Skip directory validation — NAS may be slow/offline.
	// The scanner will handle missing dirs gracefully.

	db, err := store.NewStore(cfg.DBPath)
	if err != nil {
		log.Fatalf("failed to open database: %v", err)
	}
	defer db.Close()

	if err := os.MkdirAll(cfg.CoverDir, 0755); err != nil {
		log.Fatalf("failed to create cover directory: %v", err)
	}

	// Set up CBR extraction cache directory alongside covers
	cbrCacheDir := filepath.Join(filepath.Dir(cfg.CoverDir), "cbr_cache")
	if err := os.MkdirAll(cbrCacheDir, 0755); err != nil {
		log.Fatalf("failed to create CBR cache directory: %v", err)
	}
	archive.SetCBRCacheDir(cbrCacheDir)
	archive.SetCBRCacheMaxGB(cfg.CBRCacheMaxGB)
	server.SetScanFileDelay(time.Duration(cfg.ScanFileDelayMs) * time.Millisecond)
	archive.StartCBRCacheEvictor(time.Hour)

	srv := server.NewServer(cfg.ComicDirs, db, cache.NewLRU(cfg.CacheMaxItems))
	srv.CoverDir = cfg.CoverDir
	srv.BubblesDir = cfg.BubblesDir
	srv.Password, srv.APIKey, srv.AuthOff = cfg.Password, cfg.APIKey, cfg.AuthOff

	// One shared client so enrichment and live matching draw on the same
	// 180/hr budget — two limiters would overrun ComicVine's real limit.
	var cvClient *comicvine.Client
	if cfg.ComicVineAPIKey != "" {
		enrichCacheDir := filepath.Join(filepath.Dir(cfg.CoverDir), "enrichment_cache")
		if err := os.MkdirAll(enrichCacheDir, 0755); err != nil {
			log.Fatalf("failed to create enrichment cache directory: %v", err)
		}
		cvClient = comicvine.New(cfg.ComicVineAPIKey, enrichCacheDir)
		worker := enrichment.NewWorker(db, cvClient)
		worker.Start()
		srv.Enrichment = worker
		log.Printf("enrichment: worker started (cache: %s)", enrichCacheDir)
	} else {
		log.Printf("enrichment: disabled (no GUTTER_COMICVINE_KEY / comicvine_api_key)")
	}

	if cfg.MylarURL != "" {
		arcWorker := arcsync.New(db, cfg.MylarURL)
		arcWorker.Start()
		log.Printf("arcsync: worker started (mylar: %s)", cfg.MylarURL)
	} else {
		log.Printf("arcsync: disabled (no mylar_url)")
	}

	if cfg.TaggerEnabled {
		tw := tagger.NewWorker(db, srv, tagger.Options{
			MinPages:     cfg.TaggerMinPages,
			QuietHours:   cfg.TaggerQuietHours,
			PauseBetween: time.Duration(cfg.TaggerPauseBetweenS) * time.Second,
		})
		tw.Start()
		log.Printf("tagger: worker started (min_pages=%d quiet=%q pause=%ds)",
			cfg.TaggerMinPages, cfg.TaggerQuietHours, cfg.TaggerPauseBetweenS)
	} else {
		log.Printf("tagger: disabled (tagger_enabled: false)")
	}

	var matchRunner *match.Runner
	if cfg.LocalCVDBPath != "" {
		cvDB, err := localcv.Open(cfg.LocalCVDBPath)
		if err != nil {
			log.Printf("match: disabled (opening %s: %v)", cfg.LocalCVDBPath, err)
		} else {
			defer cvDB.Close()

			creatorsIdx, err := creators.OpenIndex(
				filepath.Join(filepath.Dir(cfg.LocalCVDBPath), "creators_index.db"), cfg.LocalCVDBPath)
			if err != nil {
				log.Printf("creators: index disabled (opening: %v)", err)
			} else {
				defer creatorsIdx.Close()
				srv.Creators = creatorsIdx
				go func() {
					if err := creatorsIdx.Build(context.Background()); err != nil {
						log.Printf("creators: index build failed: %v", err)
					}
				}()
				log.Printf("creators: index build started")
			}

			cvRebuilder := arcsync.NewCVRebuilder(db, cvDB)
			if cvClient != nil {
				cvRebuilder.SetDescriptionSource(cvClient)
			}
			cvRebuilder.Start()
			defer cvRebuilder.Stop()
			log.Printf("arcsync: comicvine arc rebuilder started")

			matchRunner = match.NewRunner(db, cvDB, cfg.ComicDirs, func() int64 {
				return time.Now().UnixMilli()
			})
			if cvClient != nil {
				matchRunner.SetLive(cvClient)
				log.Printf("match: enabled (%s) with live ComicVine fill", cfg.LocalCVDBPath)
			} else {
				log.Printf("match: enabled (%s), live fill off (no comicvine_api_key)", cfg.LocalCVDBPath)
			}

			if cfg.MetronUser != "" && cfg.MetronPass != "" {
				metronCacheDir := filepath.Join(filepath.Dir(cfg.CoverDir), "metron_cache")
				if err := os.MkdirAll(metronCacheDir, 0755); err != nil {
					log.Printf("metron: disabled (cache dir: %v)", err)
				} else {
					matchRunner.SetMetron(metron.New(cfg.MetronUser, cfg.MetronPass, metronCacheDir))
					log.Printf("metron: fallback enabled (cache: %s)", metronCacheDir)
				}
			} else {
				log.Printf("metron: disabled (no metron_user/metron_pass)")
			}

			if cfg.GCDDBPath != "" {
				gcdDB, err := gcd.Open(cfg.GCDDBPath)
				if err != nil {
					log.Printf("gcd: disabled (opening %s: %v)", cfg.GCDDBPath, err)
				} else {
					defer gcdDB.Close()
					matchRunner.SetGCD(gcdDB)
					log.Printf("gcd: fallback enabled (%s)", cfg.GCDDBPath)
				}
			} else {
				log.Printf("gcd: disabled (gcd_db_path not set)")
			}
		}
	} else {
		log.Printf("match: disabled (localcv_db_path not set)")
	}

	reqLog := admin.NewRequestLog(200)
	scanState := admin.NewScanState()
	adm, err := admin.New(db, srv.Cache, cfg.ComicDirs, cfg.CoverDir, cfg.DBPath, cfg.Port, reqLog, scanState)
	if err != nil {
		log.Fatalf("failed to initialize admin console: %v", err)
	}
	srv.Admin = adm
	adm.Config = cfg
	adm.Match = matchRunner

	if len(cfg.ComicDirs) == 0 {
		log.Printf("writeback: worker not started (no comic_dirs configured)")
	} else if cfg.WritebackDisabled {
		log.Printf("writeback: worker not started (writeback_disabled: true)")
	} else {
		wbWorker := writeback.New(db, cfg.ComicDirs, cfg.CoverDir, 500*time.Millisecond, adm)
		wbWorker.Start()
		defer wbWorker.Stop()
		log.Printf("writeback: worker started (library_roots=%v)", cfg.ComicDirs)
	}
	// Push stays off unless the config carries a full push: block, so dev
	// and staging never notify the real devices.
	var notifier *notify.Notifier
	if !cfg.PushEnabled() {
		log.Printf("push: disabled (no push block)")
	} else if client, err := push.New(push.Config{
		KeyPath: cfg.PushKeyPath(), KeyID: cfg.Push.KeyID, TeamID: cfg.Push.TeamID, BundleID: cfg.Push.BundleID,
	}); err != nil {
		log.Printf("push: disabled (%v)", err)
	} else {
		notifier = &notify.Notifier{Store: db, Send: client.Send,
			DevicesPath: filepath.Join(cfg.Dir(), "push_devices.json"), Now: time.Now}
		if err := notifier.SyncDevices(); err != nil {
			log.Printf("push: syncing device mirror: %v", err)
		}
		adm.OnRun = notifier.PipelineRun
		srv.PushSecret = cfg.Push.RegisterSecret
		srv.Notifier = notifier
		log.Printf("push: enabled (key %s)", cfg.Push.KeyID)
	}
	log.Printf("pipeline: auto_pipeline=%v scan_interval=%s", cfg.AutoPipeline, cfg.ScanInterval)
	scanFn := func() {
		log.Println("scheduled scan starting...")
		if cfg.AutoPipeline {
			// Spans scan + pipeline so status polls never see an idle gap between them.
			scanState.SetPipelineRunning(true)
			defer scanState.SetPipelineRunning(false)
		}
		if err := server.ScanLibrary(cfg.ComicDirs, db, cfg.CoverDir, scanState); err != nil {
			log.Printf("scheduled scan failed: %v", err)
			notifier.ScanFailed(err)
			return // a failed scan (drive gone) must not drive the pipeline
		}
		log.Println("scheduled scan complete")
		if cfg.AutoPipeline {
			if err := adm.RunPipeline(context.Background()); err != nil {
				log.Printf("pipeline: %v", err)
			}
		}
	}
	srv.Rescan = scanFn
	scheduler := admin.NewScheduler(scanFn)
	scheduler.Start(cfg.ScanInterval)
	adm.Scheduler = scheduler
	adm.RouteCount = len(server.RouteRegistry)
	for _, r := range server.RouteRegistry {
		adm.Routes = append(adm.Routes, admin.EndpointInfo{
			Method: r.Method, Path: r.Path, Group: r.Group, Description: r.Description,
		})
	}

	// Skip scan on startup — let the scheduled scanner handle it.
	// The external drive can block the entire process with uninterruptible I/O.
	count, _ := db.SeriesCount()
	log.Printf("serving %d series from database (scan skipped, use POST /library/refresh or wait for scheduled scan)", count)

	addr := fmt.Sprintf(":%d", cfg.Port)
	log.Printf("gutter starting on %s", addr)
	log.Printf("serving comics from: %v", cfg.ComicDirs)

	httpServer := &http.Server{
		Addr:         addr,
		Handler:      srv.NewRouter(),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 5 * time.Minute, // large comic pages/downloads
		IdleTimeout:  120 * time.Second,
	}
	if err := httpServer.ListenAndServe(); err != nil {
		log.Fatalf("server failed: %v", err)
	}
}
