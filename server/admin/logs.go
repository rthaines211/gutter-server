package admin

import (
	"fmt"
	"html/template"
	"net/http"
)

type LogLine struct {
	Time    string
	Level   string
	Message string
	Color   template.CSS
}

type LogsData struct {
	Lines       []LogLine
	ActiveLevel string
	Total       int
}

func (a *Admin) handleLogs(w http.ResponseWriter, r *http.Request) {
	data := a.baseData("logs")
	data.Title = "Logs"

	levelFilter := r.URL.Query().Get("level")

	entries := a.RequestLog.Recent(100)

	lines := make([]LogLine, 0, len(entries))
	for _, e := range entries {
		level := "INFO"
		color := template.CSS("var(--text-secondary)")
		if e.StatusCode >= 400 && e.StatusCode < 500 {
			level = "WARN"
			color = "var(--amber-text)"
		} else if e.StatusCode >= 500 {
			level = "ERROR"
			color = "var(--red)"
		}

		if levelFilter != "" && level != levelFilter {
			continue
		}

		lines = append(lines, LogLine{
			Time:    e.Timestamp.Format("15:04:05"),
			Level:   level,
			Message: fmt.Sprintf("%s %s %d %dms", e.Method, e.Path, e.StatusCode, e.Duration.Milliseconds()),
			Color:   color,
		})
	}

	data.Content = LogsData{
		Lines:       lines,
		ActiveLevel: levelFilter,
		Total:       len(entries),
	}
	a.render(w, "logs.html", data)
}
