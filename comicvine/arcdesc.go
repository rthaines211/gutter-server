package comicvine

import (
	"encoding/json"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"strings"
)

// StoryArc is a ComicVine /story_arc/ detail result, trimmed to the fields
// the description sync needs.
type StoryArc struct {
	ID          int
	Name        string
	Deck        string
	Description string // raw HTML, as ComicVine returns it
}

// GetStoryArc fetches one ComicVine story arc by its bare numeric id
// (ComicVine's own URLs prefix it with the "4045-" story-arc resource type).
func (c *Client) GetStoryArc(id int) (*StoryArc, error) {
	params := url.Values{
		"format":     {"json"},
		"field_list": {"id,name,deck,description"},
	}
	env, err := c.get(fmt.Sprintf("/story_arc/4045-%d/", id), params)
	if err != nil {
		return nil, err
	}
	var raw struct {
		ID          int    `json:"id"`
		Name        string `json:"name"`
		Deck        string `json:"deck"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(env.Results, &raw); err != nil {
		return nil, err
	}
	return &StoryArc{ID: raw.ID, Name: raw.Name, Deck: raw.Deck, Description: raw.Description}, nil
}

const (
	arcDescriptionMaxChars = 2000
	// arcDescriptionMinChars: a result shorter than this from the section
	// filtering below is treated as "nothing but boilerplate survived" and
	// deck is preferred instead.
	arcDescriptionMinChars = 80
)

var (
	arcTableRe      = regexp.MustCompile(`(?is)<table[^>]*>.*?</table>`)
	arcFigureRe     = regexp.MustCompile(`(?is)<figure[^>]*>.*?</figure>`)
	arcScriptRe     = regexp.MustCompile(`(?is)<script[^>]*>.*?</script>`)
	arcStyleRe      = regexp.MustCompile(`(?is)<style[^>]*>.*?</style>`)
	arcListRe       = regexp.MustCompile(`(?is)<(?:ul|ol)[^>]*>.*?</(?:ul|ol)>`)
	arcHeadingRe    = regexp.MustCompile(`(?is)<h[1-4][^>]*>(.*?)</h[1-4]>`)
	arcBreakRe      = regexp.MustCompile(`(?i)<br\s*/?>`)
	arcBlockCloseRe = regexp.MustCompile(`(?i)</(p|div|h[1-6]|li|ul|ol|blockquote)>`)
	arcTagRe        = regexp.MustCompile(`<[^>]*>`)
	arcLineSpacesRe = regexp.MustCompile(`[ \t]+`)
	arcBlankLinesRe = regexp.MustCompile(`\n{3,}`)
)

// ComicVine story-arc descriptions are wiki pages, not prose: a heading
// matching one of these (case-insensitive substring) marks the section as
// the actual story text, to the exclusion of everything else on the page.
var synopsisHeadingKeywords = []string{"synopsis", "plot", "summary", "story", "overview", "description"}

// Without a synopsis-type heading, these sections are dropped as known
// boilerplate; anything else (plus the preamble before the first heading)
// is kept.
var excludedHeadingKeywords = []string{
	"collected editions", "related issues", "non-u.s. editions", "foreign editions",
	"creative team", "reading order", "issues", "notes", "trivia", "appearances",
	"characters", "tie-ins", "crossovers", "checklist",
}

// arcSection is the plain-text heading and raw-HTML body of one h1-h4
// section of a story-arc description.
type arcSection struct {
	heading string
	body    string
}

// splitHeadingSections splits s on its h1-h4 headings. preamble is
// everything before the first heading (the whole string, if there are no
// headings at all).
func splitHeadingSections(s string) (preamble string, secs []arcSection) {
	matches := arcHeadingRe.FindAllStringSubmatchIndex(s, -1)
	if len(matches) == 0 {
		return s, nil
	}
	preamble = s[:matches[0][0]]
	for i, m := range matches {
		bodyStart := m[1]
		bodyEnd := len(s)
		if i+1 < len(matches) {
			bodyEnd = matches[i+1][0]
		}
		secs = append(secs, arcSection{
			heading: htmlFragmentToPlainText(s[m[2]:m[3]], false),
			body:    s[bodyStart:bodyEnd],
		})
	}
	return preamble, secs
}

func headingMatchesAny(heading string, keywords []string) bool {
	h := strings.ToLower(heading)
	for _, k := range keywords {
		if strings.Contains(h, k) {
			return true
		}
	}
	return false
}

// htmlFragmentToPlainText strips tags, decodes entities, collapses
// whitespace, and turns paragraph/heading/list breaks into blank lines.
// When dropLists is true, <ul>/<ol> blocks are removed wholesale (not just
// un-tagged) rather than flattened into text.
func htmlFragmentToPlainText(s string, dropLists bool) string {
	if dropLists {
		s = arcListRe.ReplaceAllString(s, "")
	}
	s = arcBreakRe.ReplaceAllString(s, "\n")
	s = arcBlockCloseRe.ReplaceAllString(s, "\n\n")
	s = arcTagRe.ReplaceAllString(s, "")
	s = html.UnescapeString(s)

	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSpace(arcLineSpacesRe.ReplaceAllString(l, " "))
	}
	s = arcBlankLinesRe.ReplaceAllString(strings.Join(lines, "\n"), "\n\n")
	return strings.TrimSpace(s)
}

// ArcDescriptionPlainText converts a ComicVine story-arc HTML description to
// plain text. ComicVine arc pages are wiki-style, with h1-h4 sections for
// things like "Creative Team", "Collected Editions", and "Synopsis" — most
// of which is reference boilerplate, not story text. If any section heading
// looks like the actual story (synopsis/plot/summary/story/overview/
// description), only those sections' bodies are used; otherwise the text
// before the first heading plus any section not on a known boilerplate list
// is used, with list-only content (<ul>/<ol>) dropped in that fallback path.
// Headings themselves are always dropped. <table>, <figure> (image +
// caption), <script>, and <style> content is dropped entirely, and a tag
// that never closes is truncated along with everything after it. If the
// result is shorter than ~80 chars (i.e. nothing but boilerplate survived),
// deck is used instead — or the original short result if deck is also
// empty. The final text is capped at 2000 chars on a word boundary.
func ArcDescriptionPlainText(description, deck string) string {
	cleanDeck := htmlFragmentToPlainText(deck, false)
	if strings.TrimSpace(description) == "" {
		return capAtWordBoundary(cleanDeck, arcDescriptionMaxChars)
	}

	s := description
	s = arcTableRe.ReplaceAllString(s, "")
	s = arcFigureRe.ReplaceAllString(s, "")
	s = arcScriptRe.ReplaceAllString(s, "")
	s = arcStyleRe.ReplaceAllString(s, "")
	s = truncateAtUnclosedTag(s)

	preamble, secs := splitHeadingSections(s)

	synopsisFound := false
	for _, sec := range secs {
		if headingMatchesAny(sec.heading, synopsisHeadingKeywords) {
			synopsisFound = true
			break
		}
	}

	var parts []string
	if synopsisFound {
		for _, sec := range secs {
			if !headingMatchesAny(sec.heading, synopsisHeadingKeywords) {
				continue
			}
			if t := htmlFragmentToPlainText(sec.body, false); t != "" {
				parts = append(parts, t)
			}
		}
	} else {
		if t := htmlFragmentToPlainText(preamble, true); t != "" {
			parts = append(parts, t)
		}
		for _, sec := range secs {
			if headingMatchesAny(sec.heading, excludedHeadingKeywords) {
				continue
			}
			if t := htmlFragmentToPlainText(sec.body, true); t != "" {
				parts = append(parts, t)
			}
		}
	}

	text := strings.TrimSpace(strings.Join(parts, "\n\n"))
	if len(text) < arcDescriptionMinChars && cleanDeck != "" {
		text = cleanDeck
	}
	return capAtWordBoundary(text, arcDescriptionMaxChars)
}

// truncateAtUnclosedTag drops a trailing "<..." that never reaches a
// closing '>' (a truncated or malformed fragment), so it can't leak through
// arcTagRe — which requires a closing '>' to match — and show up as literal
// text. Anything after the last '>' in the whole string has, by definition,
// no '>' to close it, so a '<' there is cut along with everything after it.
func truncateAtUnclosedTag(s string) string {
	offset := 0
	tail := s
	if lastClose := strings.LastIndex(s, ">"); lastClose >= 0 {
		offset = lastClose + 1
		tail = s[offset:]
	}
	if idx := strings.IndexByte(tail, '<'); idx != -1 {
		return s[:offset+idx]
	}
	return s
}

func capAtWordBoundary(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := s[:max]
	if idx := strings.LastIndexAny(cut, " \n"); idx > 0 {
		cut = cut[:idx]
	}
	return strings.TrimSpace(cut)
}
