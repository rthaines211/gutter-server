package admin

import (
	"net/http"
	"sort"
	"strings"

	"github.com/rthaines211/gutter-server/store"
)

type LibraryData struct {
	View       string
	Query      string
	Status     string
	Publisher  string
	Series     []LibrarySeries
	Publishers []string
	Total      int

	// Shelves is the grid view grouped by publisher — the design's default
	// "Shelves by publisher" mode. Built from the same filtered Series slice,
	// so it always agrees with the table view's row set.
	Shelves  []Shelf
	NewCount int
}

// Shelf is one publisher's row of covers, with the totals its header shows.
type Shelf struct {
	Name   string
	Series []LibrarySeries
	Issues int
}

type LibrarySeries struct {
	store.Series
	Genres   []string
	CoverURL string
	IsNew    bool
	Index    int
}

func (a *Admin) handleLibrary(w http.ResponseWriter, r *http.Request) {
	data := a.baseData("library")
	data.Title = "Library"

	view := r.URL.Query().Get("view")
	if view != "table" {
		view = "grid"
	}
	query := r.URL.Query().Get("q")
	status := r.URL.Query().Get("status")
	publisher := r.URL.Query().Get("publisher")

	allSeries, _ := a.Store.ListSeries()

	pubSet := make(map[string]bool)
	for _, s := range allSeries {
		if s.Publisher != "" {
			pubSet[s.Publisher] = true
		}
	}
	publishers := make([]string, 0, len(pubSet))
	for p := range pubSet {
		publishers = append(publishers, p)
	}

	items := make([]LibrarySeries, 0, len(allSeries))
	idx := 0

	for _, s := range allSeries {
		if query != "" {
			q := strings.ToLower(query)
			if !strings.Contains(strings.ToLower(s.Name), q) &&
				!strings.Contains(strings.ToLower(s.Writer), q) &&
				!strings.Contains(strings.ToLower(s.Publisher), q) {
				continue
			}
		}

		if publisher != "" && s.Publisher != publisher {
			continue
		}

		genres, _ := a.Store.ListSeriesGenres(s.ID)
		if genres == nil {
			genres = []string{}
		}

		coverURL := ""
		if s.CoverPath != "" {
			coverURL = "/library/" + s.ID + "/cover"
		}

		ls := LibrarySeries{
			Series:   s,
			Genres:   genres,
			CoverURL: coverURL,
			IsNew:    s.Year >= 2024,
			Index:    idx,
		}

		if status != "" && status != "all" {
			switch status {
			case "new":
				if !ls.IsNew {
					continue
				}
			}
		}

		items = append(items, ls)
		idx++
	}

	// Group into shelves, publisher A–Z with unknowns last, so the default
	// view has a stable order rather than the store's.
	byPub := make(map[string][]LibrarySeries)
	newCount := 0
	for _, ls := range items {
		name := ls.Publisher
		if name == "" {
			name = "Unknown publisher"
		}
		byPub[name] = append(byPub[name], ls)
		if ls.IsNew {
			newCount++
		}
	}
	shelfNames := make([]string, 0, len(byPub))
	for name := range byPub {
		shelfNames = append(shelfNames, name)
	}
	sort.Slice(shelfNames, func(i, j int) bool {
		a, b := shelfNames[i], shelfNames[j]
		if (a == "Unknown publisher") != (b == "Unknown publisher") {
			return b == "Unknown publisher"
		}
		return strings.ToLower(a) < strings.ToLower(b)
	})
	shelves := make([]Shelf, 0, len(shelfNames))
	for _, name := range shelfNames {
		issues := 0
		for _, ls := range byPub[name] {
			issues += ls.IssueCount
		}
		shelves = append(shelves, Shelf{Name: name, Series: byPub[name], Issues: issues})
	}

	sort.Strings(publishers)

	data.Content = LibraryData{
		View:       view,
		Query:      query,
		Status:     status,
		Publisher:  publisher,
		Series:     items,
		Publishers: publishers,
		Total:      len(allSeries),
		Shelves:    shelves,
		NewCount:   newCount,
	}

	a.render(w, "library.html", data)
}
