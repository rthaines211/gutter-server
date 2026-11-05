package admin

import (
	"net/http"
	"time"

	"github.com/ryanhaines/comic-reader/store"
)

type ActivityRun struct {
	store.PipelineRun
	When      string
	Duration  string
	Attention bool // flagged / review / misfiled / error present
}

type ActivityData struct {
	Runs []ActivityRun
}

func attention(r store.PipelineRun) bool {
	if r.SkippedReason != "" {
		return true
	}
	for _, e := range r.Events {
		switch e.Kind {
		case "flagged", "sent_to_review", "misfiled", "error", "unbound":
			return true
		}
	}
	return false
}

func (a *Admin) handleActivity(w http.ResponseWriter, r *http.Request) {
	data := a.baseData("activity")
	data.Title = "Activity"
	runs, _ := a.Store.ListPipelineRuns(100)
	out := make([]ActivityRun, 0, len(runs))
	for _, run := range runs {
		out = append(out, ActivityRun{
			PipelineRun: run,
			When:        time.UnixMilli(run.StartedAt).Format("Jan 2 15:04"),
			Duration:    (time.Duration(run.FinishedAt-run.StartedAt) * time.Millisecond).Round(time.Second).String(),
			Attention:   attention(run),
		})
	}
	data.Content = ActivityData{Runs: out}
	a.render(w, "activity.html", data)
}
