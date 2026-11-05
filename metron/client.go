// Package metron is a minimal Metron (metron.cloud) API client used as a
// fallback where ComicVine can't match: series lookup (by name or ComicVine
// cross-reference id), paginated issue lists, and issue detail with typed
// credits. Responses are rate-limited (30/min, Metron's documented cap) and
// cached on disk keyed by URL, so re-runs never re-spend API budget.
package metron

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

var ErrNotFound = errors.New("metron: not found")

type Series struct {
	ID         int
	Name       string
	YearBegan  int
	Publisher  string
	Desc       string
	IssueCount int
	CVID       int // ComicVine volume id; 0 = no cross-reference
}

type SeriesResult struct {
	ID         int
	Display    string
	YearBegan  int
	IssueCount int
}

type Credit struct {
	Creator string
	Roles   []string // lowercased
}

type IssueResult struct {
	ID        int
	Number    string
	CoverDate string
}

type Issue struct {
	ID        int
	Number    string
	Title     string
	Desc      string
	CoverDate string
	CVID      int
	Credits   []Credit
	SeriesID  int
}

type Client struct {
	BaseURL  string
	user     string
	pass     string
	cacheDir string
	http     *http.Client
	limiter  *tokenBucket
}

func New(user, pass, cacheDir string) *Client {
	os.MkdirAll(cacheDir, 0o755) // best-effort, same as comicvine.New
	return &Client{
		BaseURL:  "https://metron.cloud/api",
		user:     user,
		pass:     pass,
		cacheDir: cacheDir,
		http:     &http.Client{Timeout: 30 * time.Second},
		limiter:  newTokenBucket(30, 30.0/60.0),
	}
}

// listEnvelope is Metron's DRF pagination wrapper.
type listEnvelope struct {
	Count   int             `json:"count"`
	Next    *string         `json:"next"`
	Results json.RawMessage `json:"results"`
}

// get fetches path?params with Basic auth, honoring disk cache and rate
// limit. Auth travels in the header, never the URL, so cache keys are
// credential-independent (same scheme as comicvine.Client.get).
func (c *Client) get(path string, params url.Values) ([]byte, error) {
	full := path
	if enc := params.Encode(); enc != "" {
		full += "?" + enc
	}
	cacheKey := sha256.Sum256([]byte(full))
	cachePath := filepath.Join(c.cacheDir, hex.EncodeToString(cacheKey[:])+".json")
	if body, err := os.ReadFile(cachePath); err == nil {
		return body, nil
	}
	c.limiter.wait()
	req, err := http.NewRequest("GET", c.BaseURL+full, nil)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(c.user, c.pass)
	req.Header.Set("User-Agent", "comic-reader-enrichment/1.0 (self-hosted)")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("metron: HTTP %d for %s", resp.StatusCode, path)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	os.WriteFile(cachePath, body, 0o644) // best-effort cache write
	return body, nil
}

// rawSeriesDetail mirrors GET /series/{id}/.
type rawSeriesDetail struct {
	ID         int    `json:"id"`
	Name       string `json:"name"`
	YearBegan  int    `json:"year_began"`
	Desc       string `json:"desc"`
	IssueCount int    `json:"issue_count"`
	Publisher  struct {
		Name string `json:"name"`
	} `json:"publisher"`
	CVID int `json:"cv_id"`
}

func (r rawSeriesDetail) series() *Series {
	return &Series{
		ID: r.ID, Name: r.Name, YearBegan: r.YearBegan, Desc: r.Desc,
		IssueCount: r.IssueCount, Publisher: r.Publisher.Name, CVID: r.CVID,
	}
}

func (c *Client) GetSeries(id int) (*Series, error) {
	body, err := c.get("/series/"+strconv.Itoa(id)+"/", nil)
	if err != nil {
		return nil, err
	}
	var raw rawSeriesDetail
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("metron: bad series JSON: %w", err)
	}
	return raw.series(), nil
}

// rawSeriesResult mirrors a GET /series/ list item.
type rawSeriesResult struct {
	ID         int    `json:"id"`
	Series     string `json:"series"`
	YearBegan  int    `json:"year_began"`
	IssueCount int    `json:"issue_count"`
}

func (c *Client) SearchSeries(name string) ([]SeriesResult, error) {
	body, err := c.get("/series/", url.Values{"name": {name}})
	if err != nil {
		return nil, err
	}
	var env listEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("metron: bad series list JSON: %w", err)
	}
	var raws []rawSeriesResult
	if err := json.Unmarshal(env.Results, &raws); err != nil {
		return nil, fmt.Errorf("metron: bad series results JSON: %w", err)
	}
	out := make([]SeriesResult, 0, len(raws))
	for _, r := range raws {
		out = append(out, SeriesResult{
			ID: r.ID, Display: r.Series, YearBegan: r.YearBegan, IssueCount: r.IssueCount,
		})
	}
	return out, nil
}

// SeriesByCVID resolves a Metron series through its ComicVine volume
// cross-reference — the pivot that lets Metron data feed our CV-keyed tables.
func (c *Client) SeriesByCVID(cvID int) (*Series, error) {
	body, err := c.get("/series/", url.Values{"cv_id": {strconv.Itoa(cvID)}})
	if err != nil {
		return nil, err
	}
	var env listEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("metron: bad series list JSON: %w", err)
	}
	var raws []rawSeriesResult
	if err := json.Unmarshal(env.Results, &raws); err != nil {
		return nil, fmt.Errorf("metron: bad series results JSON: %w", err)
	}
	if len(raws) == 0 {
		return nil, ErrNotFound
	}
	return c.GetSeries(raws[0].ID)
}

// rawIssueResult mirrors a GET /issue/ list item.
type rawIssueResult struct {
	ID        int    `json:"id"`
	Number    string `json:"number"`
	CoverDate string `json:"cover_date"`
}

// ListSeriesIssues walks every page of GET /issue/?series_id=. Metron's
// "next" is a full URL; we only need its page number — request pages by
// index until next is null.
func (c *Client) ListSeriesIssues(seriesID int) ([]IssueResult, error) {
	var all []IssueResult
	for page := 1; ; page++ {
		params := url.Values{"series_id": {strconv.Itoa(seriesID)}}
		if page > 1 {
			params.Set("page", strconv.Itoa(page))
		}
		body, err := c.get("/issue/", params)
		if err != nil {
			return nil, err
		}
		var env listEnvelope
		if err := json.Unmarshal(body, &env); err != nil {
			return nil, fmt.Errorf("metron: bad issue list JSON: %w", err)
		}
		var raws []rawIssueResult
		if err := json.Unmarshal(env.Results, &raws); err != nil {
			return nil, fmt.Errorf("metron: bad issue results JSON: %w", err)
		}
		for _, r := range raws {
			all = append(all, IssueResult{ID: r.ID, Number: r.Number, CoverDate: r.CoverDate})
		}
		if env.Next == nil || len(raws) == 0 {
			return all, nil
		}
	}
}

// rawIssueDetail mirrors GET /issue/{id}/.
type rawIssueDetail struct {
	ID     int `json:"id"`
	Series struct {
		ID int `json:"id"`
	} `json:"series"`
	Number    string   `json:"number"`
	Name      []string `json:"name"` // story titles
	Desc      string   `json:"desc"`
	CoverDate string   `json:"cover_date"`
	CVID      int      `json:"cv_id"`
	Credits   []struct {
		Creator string `json:"creator"`
		Role    []struct {
			Name string `json:"name"`
		} `json:"role"`
	} `json:"credits"`
}

func (c *Client) GetIssue(id int) (*Issue, error) {
	body, err := c.get("/issue/"+strconv.Itoa(id)+"/", nil)
	if err != nil {
		return nil, err
	}
	var raw rawIssueDetail
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("metron: bad issue JSON: %w", err)
	}
	iss := &Issue{
		ID: raw.ID, Number: raw.Number, Desc: raw.Desc,
		CoverDate: raw.CoverDate, CVID: raw.CVID, SeriesID: raw.Series.ID,
		Title: strings.Join(raw.Name, "; "),
	}
	for _, cr := range raw.Credits {
		roles := make([]string, 0, len(cr.Role))
		for _, r := range cr.Role {
			roles = append(roles, strings.ToLower(r.Name))
		}
		iss.Credits = append(iss.Credits, Credit{Creator: cr.Creator, Roles: roles})
	}
	return iss, nil
}
