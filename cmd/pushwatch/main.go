// Command pushwatch runs on the host Mac from launchd and pushes "gutter
// is down" when /healthz keeps failing — the one alert the server can't send
// itself. One tick per run; launchd supplies the interval.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/rthaines211/gutter-server/config"
	"github.com/rthaines211/gutter-server/push"
)

func main() {
	home, _ := os.UserHomeDir()
	cfgPath := flag.String("config", "config.yaml", "host path to the server's config.yaml")
	url := flag.String("url", "", "public healthz URL, e.g. https://comics.example.com/healthz (required)")
	statePath := flag.String("state", filepath.Join(home, "Library/Application Support/comic-pushwatch/state.json"), "state file")
	flag.Parse()
	if *url == "" {
		log.Fatal("pushwatch: -url is required (the server's public /healthz URL)")
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("pushwatch: %v", err)
	}
	if !cfg.PushKeyConfigured() {
		log.Printf("pushwatch: push disabled in %s; nothing to do", *cfgPath)
		return
	}

	var s State
	if b, err := os.ReadFile(*statePath); err == nil {
		json.Unmarshal(b, &s)
	} else if !errors.Is(err, fs.ErrNotExist) {
		log.Printf("pushwatch: reading state: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	healthy, detail := Check(ctx, *url)
	cancel()
	s, msg := Step(s, healthy, detail)
	if !healthy {
		log.Printf("pushwatch: unhealthy (%d in a row): %s", s.Fails, detail)
	}

	if msg != nil {
		client, err := push.New(push.Config{KeyPath: cfg.PushKeyPath(), KeyID: cfg.Push.KeyID,
			TeamID: cfg.Push.TeamID, BundleID: cfg.Push.BundleID})
		if err != nil {
			log.Fatalf("pushwatch: %v", err) // state unsaved: the alert retries next tick
		}
		devices, err := push.ReadDevices(filepath.Join(cfg.Dir(), "push_devices.json"))
		if err != nil {
			log.Fatalf("pushwatch: reading devices: %v", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		for _, d := range devices {
			if err := client.Send(ctx, d, *msg); err != nil {
				log.Printf("pushwatch: send to %.8s…: %v", d.Token, err)
			}
		}
		cancel()
		log.Printf("pushwatch: sent %q to %d device(s)", msg.Title, len(devices))
	}

	os.MkdirAll(filepath.Dir(*statePath), 0o755)
	b, _ := json.Marshal(s)
	if err := os.WriteFile(*statePath, b, 0o644); err != nil {
		log.Printf("pushwatch: writing state: %v", err)
	}
}
