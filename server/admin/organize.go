package admin

import (
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/rthaines211/gutter-server/organize"
)

func (a *Admin) organizer() *organize.Organizer {
	return organize.New(a.Store, a.Match, a.ComicDirs, a.CoverDir)
}

// OrganizeData is the view model for organize.html.
type OrganizeData struct {
	Enabled bool
	Plan    *organize.Plan
	// SeriesRows mirrors Plan.Series but with each card's op rows capped —
	// see opCap — so a huge reorg doesn't render hundreds of rows by default.
	SeriesRows []OrganizeSeriesRow
	Report     *organize.Report // set after an apply redirects back with ?applied=...
	Error      string
	// Busy explains a refused apply. The gate is held by scan/match/writeback,
	// and the handler used to redirect with no params at all, which rendered an
	// unchanged page and looked exactly like a dead button.
	Busy string
	// Templates backs the inline editor card: the same rename_templates the
	// planner resolves filenames with, editable next to their preview.
	Templates []TemplateRow
}

// TemplateRow is one rename template as shown in the editor card.
type TemplateRow struct {
	Format   string
	Template string
}

// OrganizeSeriesRow is one series card's view model: the plan's ops for that
// series, capped like match_detail.html's issueCap pattern.
type OrganizeSeriesRow struct {
	organize.SeriesPlan
	VisibleOps []organize.Op
	ShowAll    bool
	MoreCount  int
}

// opCap is how many op rows render per series card by default; ?all=1 shows
// the rest (on every card — simplest reading of the pattern for a page that
// lists many series at once, unlike match_detail's single-series page).
const opCap = 10

func capOps(sp organize.SeriesPlan, showAll bool) OrganizeSeriesRow {
	row := OrganizeSeriesRow{SeriesPlan: sp}
	row.ShowAll = showAll || len(sp.Ops) <= opCap
	if row.ShowAll {
		row.VisibleOps = sp.Ops
	} else {
		row.VisibleOps = sp.Ops[:opCap]
	}
	row.MoreCount = len(sp.Ops) - len(row.VisibleOps)
	return row
}

func (a *Admin) handleOrganize(w http.ResponseWriter, r *http.Request) {
	data := a.baseData("organize")
	data.Title = "Organize Library"
	content := OrganizeData{Enabled: a.Match != nil}
	if a.Match != nil {
		plan, err := a.organizer().Plan()
		if err != nil {
			content.Error = err.Error()
		} else {
			content.Plan = plan
			showAll := r.URL.Query().Get("all") == "1"
			for _, sp := range plan.Series {
				content.SeriesRows = append(content.SeriesRows, capOps(sp, showAll))
			}
		}
	}
	if tpls, err := a.Store.ListRenameTemplates(); err == nil {
		content.Templates = templateRows(tpls)
	}
	content.Busy = r.URL.Query().Get("busy")
	if applied := r.URL.Query().Get("applied"); applied != "" {
		rep := organize.Report{}
		rep.Applied, _ = strconv.Atoi(applied)
		rep.Skipped, _ = strconv.Atoi(r.URL.Query().Get("skipped"))
		if errs := r.URL.Query().Get("errs"); errs != "" {
			rep.Errors = strings.Split(errs, "\n")
		}
		content.Report = &rep
	}
	data.Content = content
	a.render(w, "organize.html", data)
}

// templateRows orders the template map for stable rendering: "default" first
// (it's the fallback every other format collapses to), the rest alphabetical.
func templateRows(m map[string]string) []TemplateRow {
	rows := make([]TemplateRow, 0, len(m))
	for f, tpl := range m {
		rows = append(rows, TemplateRow{Format: f, Template: tpl})
	}
	sort.Slice(rows, func(i, j int) bool {
		if (rows[i].Format == "default") != (rows[j].Format == "default") {
			return rows[i].Format == "default"
		}
		return rows[i].Format < rows[j].Format
	})
	return rows
}

// handleOrganizeTemplates saves edits from the inline template card. Only
// formats that already exist are submitted (the card has no add-new row), and
// blank values are ignored rather than wiping a template.
func (a *Admin) handleOrganizeTemplates(w http.ResponseWriter, r *http.Request) {
	existing, err := a.Store.ListRenameTemplates()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	updated := map[string]string{}
	for format := range existing {
		if v := strings.TrimSpace(r.FormValue("tpl_" + format)); v != "" && v != existing[format] {
			updated[format] = v
		}
	}
	if len(updated) > 0 {
		if err := a.Store.SaveRenameTemplates(updated); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	http.Redirect(w, r, "/_admin/organize", http.StatusSeeOther)
}

func (a *Admin) handleOrganizeApply(w http.ResponseWriter, r *http.Request) {
	if a.Match == nil {
		http.Error(w, "matching is disabled: set localcv_db_path", http.StatusPreconditionFailed)
		return
	}
	if !a.canStartOrganize() {
		http.Redirect(w, r, "/_admin/organize?busy="+url.QueryEscape(a.busyReason()), http.StatusSeeOther)
		return
	}
	defer a.organizeRunning.Store(false)
	if a.ScanState != nil {
		a.ScanState.SetOrganizeRunning(true)
		defer a.ScanState.SetOrganizeRunning(false)
	}

	rep, err := a.organizer().Apply(r.FormValue("series_id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Surface the report via query params — the page re-plans on GET anyway.
	q := "?applied=" + strconv.Itoa(rep.Applied) + "&skipped=" + strconv.Itoa(rep.Skipped)
	if len(rep.Errors) > 0 {
		q += "&errs=" + url.QueryEscape(strings.Join(rep.Errors, "\n"))
	}
	http.Redirect(w, r, "/_admin/organize"+q, http.StatusSeeOther)
}
