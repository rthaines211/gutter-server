// Package comicvine is a minimal ComicVine API client: volume search,
// volume detail (with creators), and paginated issue lists. Responses are
// rate-limited (180/hr) and cached on disk keyed by URL-without-api-key,
// so re-runs and re-matching never re-spend API budget.
package comicvine

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type Volume struct {
	ID            int
	Name          string
	StartYear     int
	Publisher     string
	CountOfIssues int
	CoverURL      string
	Description   string // plain text, HTML stripped
}

type Creator struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Role string `json:"role"`
}

type VolumeDetail struct {
	Volume
	Creators []Creator
}

type Issue struct {
	ID        int
	Number    string
	Title     string
	Summary   string // plain text
	CoverDate string // "2012-03-14"
}

type Client struct {
	BaseURL  string
	apiKey   string
	cacheDir string
	http     *http.Client
	limiter  *tokenBucket
}

func New(apiKey, cacheDir string) *Client {
	// Cache writes are best-effort; without the dir they'd all silently
	// fail and every re-run would re-spend API budget.
	os.MkdirAll(cacheDir, 0o755)
	return &Client{
		BaseURL:  "https://comicvine.gamespot.com/api",
		apiKey:   apiKey,
		cacheDir: cacheDir,
		http:     &http.Client{Timeout: 30 * time.Second},
		limiter:  newTokenBucket(180, 180.0/3600.0),
	}
}

// Available reports remaining request budget (for /enrichment/status).
func (c *Client) Available() int { return c.limiter.available() }

type envelope struct {
	StatusCode           int             `json:"status_code"`
	Error                string          `json:"error"`
	NumberOfTotalResults int             `json:"number_of_total_results"`
	Results              json.RawMessage `json:"results"`
}

// get fetches path?params, honoring the disk cache and rate limit. The
// api_key is appended after the cache key is computed so cache files are
// key-independent. Only status_code==1 envelopes are cached.
func (c *Client) get(path string, params url.Values) (*envelope, error) {
	cacheKey := sha256.Sum256([]byte(path + "?" + params.Encode()))
	cachePath := filepath.Join(c.cacheDir, hex.EncodeToString(cacheKey[:])+".json")
	body, err := os.ReadFile(cachePath)
	cached := err == nil
	if !cached {
		c.limiter.wait()
		params.Set("api_key", c.apiKey)
		req, err := http.NewRequest("GET", c.BaseURL+path+"?"+params.Encode(), nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", "gutter-enrichment/1.0 (self-hosted)")
		resp, err := c.http.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("comicvine: HTTP %d for %s", resp.StatusCode, path)
		}
		body, err = io.ReadAll(resp.Body)
		if err != nil {
			return nil, err
		}
	}
	var env envelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("comicvine: bad JSON for %s: %w", path, err)
	}
	if env.StatusCode != 1 {
		return nil, fmt.Errorf("comicvine: API error %d: %s", env.StatusCode, env.Error)
	}
	if !cached {
		os.WriteFile(cachePath, body, 0o644) // best-effort cache write
	}
	return &env, nil
}

type rawVolume struct {
	ID            int    `json:"id"`
	Name          string `json:"name"`
	StartYear     string `json:"start_year"`
	CountOfIssues int    `json:"count_of_issues"`
	Description   string `json:"description"`
	Publisher     *struct {
		Name string `json:"name"`
	} `json:"publisher"`
	Image *struct {
		MediumURL string `json:"medium_url"`
	} `json:"image"`
	PersonCredits []Creator `json:"person_credits"`
}

func (r rawVolume) toVolume() Volume {
	year, _ := strconv.Atoi(strings.TrimSpace(r.StartYear))
	v := Volume{
		ID:            r.ID,
		Name:          r.Name,
		StartYear:     year,
		CountOfIssues: r.CountOfIssues,
		Description:   StripHTML(r.Description),
	}
	if r.Publisher != nil {
		v.Publisher = r.Publisher.Name
	}
	if r.Image != nil {
		v.CoverURL = r.Image.MediumURL
	}
	return v
}

func (c *Client) SearchVolumes(name string) ([]Volume, error) {
	params := url.Values{
		"format":     {"json"},
		"resources":  {"volume"},
		"query":      {name},
		"limit":      {"10"},
		"field_list": {"id,name,start_year,publisher,count_of_issues,image,description"},
	}
	env, err := c.get("/search/", params)
	if err != nil {
		return nil, err
	}
	var raws []rawVolume
	if err := json.Unmarshal(env.Results, &raws); err != nil {
		return nil, err
	}
	vols := make([]Volume, 0, len(raws))
	for _, r := range raws {
		vols = append(vols, r.toVolume())
	}
	return vols, nil
}

func (c *Client) GetVolume(id int) (*VolumeDetail, error) {
	params := url.Values{
		"format":     {"json"},
		"field_list": {"id,name,start_year,publisher,count_of_issues,image,description,person_credits"},
	}
	env, err := c.get(fmt.Sprintf("/volume/4050-%d/", id), params)
	if err != nil {
		return nil, err
	}
	var raw rawVolume
	if err := json.Unmarshal(env.Results, &raw); err != nil {
		return nil, err
	}
	return &VolumeDetail{Volume: raw.toVolume(), Creators: raw.PersonCredits}, nil
}

type rawIssue struct {
	ID          int    `json:"id"`
	IssueNumber string `json:"issue_number"`
	Name        string `json:"name"`
	Description string `json:"description"`
	CoverDate   string `json:"cover_date"`
}

func (c *Client) ListVolumeIssues(volumeID int) ([]Issue, error) {
	var all []Issue
	for offset := 0; ; offset += 100 {
		params := url.Values{
			"format":     {"json"},
			"filter":     {fmt.Sprintf("volume:%d", volumeID)},
			"field_list": {"id,issue_number,name,description,cover_date"},
			"limit":      {"100"},
			"offset":     {strconv.Itoa(offset)},
		}
		env, err := c.get("/issues/", params)
		if err != nil {
			return nil, err
		}
		var raws []rawIssue
		if err := json.Unmarshal(env.Results, &raws); err != nil {
			return nil, err
		}
		for _, r := range raws {
			all = append(all, Issue{
				ID:        r.ID,
				Number:    r.IssueNumber,
				Title:     r.Name,
				Summary:   StripHTML(r.Description),
				CoverDate: r.CoverDate,
			})
		}
		if len(raws) == 0 || offset+100 >= env.NumberOfTotalResults {
			break
		}
	}
	return all, nil
}

var (
	tagRe   = regexp.MustCompile(`<[^>]*>`)
	spaceRe = regexp.MustCompile(`\s+`)
)

// StripHTML turns ComicVine HTML descriptions into trimmed plain text.
func StripHTML(s string) string {
	if s == "" {
		return ""
	}
	s = tagRe.ReplaceAllString(s, " ")
	s = html.UnescapeString(s)
	return strings.TrimSpace(spaceRe.ReplaceAllString(s, " "))
}
