package admin

import "net/http"

type ScannerData struct {
	Scan         ScanSnapshot
	Interval     string
	IntervalOpts []IntervalOption
}

type IntervalOption struct {
	Value  string
	Label  string
	Active bool
}

func (a *Admin) handleScanner(w http.ResponseWriter, r *http.Request) {
	data := a.baseData("scanner")
	data.Title = "Scanner"

	interval := ""
	if a.Scheduler != nil {
		interval = a.Scheduler.Interval()
	}

	opts := []IntervalOption{
		{"off", "Manual", interval == ""},
		{"30m0s", "30 min", interval == "30m0s"},
		{"1h0m0s", "1 hour", interval == "1h0m0s"},
		{"6h0m0s", "6 hours", interval == "6h0m0s"},
		{"24h0m0s", "24 hours", interval == "24h0m0s"},
	}

	data.Content = ScannerData{
		Scan:         a.ScanState.Snapshot(),
		Interval:     interval,
		IntervalOpts: opts,
	}
	a.render(w, "scanner.html", data)
}

func (a *Admin) handleSetSchedule(w http.ResponseWriter, r *http.Request) {
	interval := r.FormValue("interval")
	if a.Scheduler != nil {
		a.Scheduler.UpdateInterval(interval)
	}
	if a.Config != nil {
		a.Config.SetScanInterval(interval)
	}
	http.Redirect(w, r, "/_admin/scanner", http.StatusSeeOther)
}
