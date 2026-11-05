package comicvine

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

const storyArcFixture = `{
	"error": "OK", "status_code": 1, "number_of_total_results": 1,
	"results": {
		"id": 57081, "name": "Dark Reign", "deck": "The Dark Reign begins.",
		"description": "<h2>Overview</h2><p>This is the <a href=\"http://example.com\">story arc</a> description. It spans multiple sentences &amp; more.</p><figure><img src=\"x.jpg\"/><figcaption>Cover image caption</figcaption></figure><h2>Issues</h2><table><tr><td>Issue 1</td><td>2012</td></tr></table><p>Final   paragraph.</p>"
	}
}`

func TestGetStoryArc(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/story_arc/4045-57081/") {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		fmt.Fprint(w, storyArcFixture)
	}))

	sa, err := c.GetStoryArc(57081)
	if err != nil {
		t.Fatal(err)
	}
	if sa.ID != 57081 || sa.Name != "Dark Reign" || sa.Deck != "The Dark Reign begins." {
		t.Fatalf("unexpected story arc %+v", sa)
	}
	if !strings.Contains(sa.Description, "<h2>Overview</h2>") {
		t.Fatalf("expected raw HTML description preserved, got %q", sa.Description)
	}
}

func TestArcDescriptionPlainText(t *testing.T) {
	got := ArcDescriptionPlainText(storyArcHTML, "fallback deck")

	if strings.Contains(got, "<") || strings.Contains(got, ">") {
		t.Fatalf("expected tags stripped, got %q", got)
	}
	if strings.Contains(got, "Cover image caption") {
		t.Fatalf("expected figure/figcaption dropped, got %q", got)
	}
	if strings.Contains(got, "Issue 1") || strings.Contains(got, "2012") {
		t.Fatalf("expected table content dropped, got %q", got)
	}
	// Headings are always dropped — and since "Overview" is a synopsis-type
	// heading, only its body is kept; "Issues" (not a synopsis heading) is
	// excluded entirely, body text and all.
	if strings.Contains(got, "Overview") || strings.Contains(got, "Issues") {
		t.Fatalf("expected headings dropped, got %q", got)
	}
	if strings.Contains(got, "Final") {
		t.Fatalf("expected non-synopsis section excluded once a synopsis section exists, got %q", got)
	}
	if !strings.Contains(got, "story arc description") {
		t.Fatalf("expected link text preserved, got %q", got)
	}
	if !strings.Contains(got, "spans multiple sentences & more") {
		t.Fatalf("expected entity decoded, got %q", got)
	}
	if !strings.Contains(got, "more.\n\nA second paragraph") {
		t.Fatalf("expected paragraphs within a kept section separated by a blank line, got %q", got)
	}
	if strings.Contains(got, "second  paragraph") {
		t.Fatalf("expected intra-line whitespace collapsed, got %q", got)
	}
}

const storyArcHTML = `<h2>Overview</h2><p>This is the <a href="http://example.com">story arc</a> description. It spans multiple sentences &amp; more.</p><p>A second  paragraph continues the story.</p><figure><img src="x.jpg"/><figcaption>Cover image caption</figcaption></figure><h2>Issues</h2><table><tr><td>Issue 1</td><td>2012</td></tr></table><p>Final   paragraph.</p>`

func TestArcDescriptionPlainTextPrefersSynopsisSection(t *testing.T) {
	html := `<h2>Creative Team</h2>` +
		`<p>Writer(s) - Joss Whedon</p><p>Penciler(s) - John Cassaday</p>` +
		`<h2>Synopsis</h2>` +
		`<p>The X-Men face a new danger from an old enemy returning.</p>` +
		`<p>Secrets are revealed as the team splits apart.</p>`

	got := ArcDescriptionPlainText(html, "Astonishing X-Men: Dangerous deck.")

	if strings.Contains(got, "Writer") || strings.Contains(got, "Joss Whedon") || strings.Contains(got, "Creative Team") {
		t.Fatalf("expected Creative Team boilerplate excluded, got %q", got)
	}
	if !strings.Contains(got, "new danger from an old enemy") {
		t.Fatalf("expected synopsis text present, got %q", got)
	}
	if !strings.Contains(got, "Secrets are revealed") {
		t.Fatalf("expected both synopsis paragraphs present, got %q", got)
	}
}

func TestArcDescriptionPlainTextFallsBackToDeckWhenOnlyBoilerplate(t *testing.T) {
	html := `<h2>Collected Editions</h2>` +
		`<ul><li>Astonishing X-Men: Dangerous</li></ul>` +
		`<h2>Related Issues</h2>` +
		`<ul><li>Astonishing X-Men #13</li><li>Astonishing X-Men #14</li></ul>`

	deck := "A desperate mission to stop an old enemy."
	got := ArcDescriptionPlainText(html, deck)

	if got != deck {
		t.Fatalf("expected boilerplate-only sections to fall back to deck, got %q", got)
	}
}

func TestArcDescriptionPlainTextFallsBackToDeck(t *testing.T) {
	got := ArcDescriptionPlainText("", "A deck-only summary.")
	if got != "A deck-only summary." {
		t.Fatalf("expected deck fallback, got %q", got)
	}
	got = ArcDescriptionPlainText("   ", "A deck-only summary.")
	if got != "A deck-only summary." {
		t.Fatalf("expected deck fallback on blank description, got %q", got)
	}
}

func TestArcDescriptionPlainTextDropsScriptAndStyle(t *testing.T) {
	html := `<p>Before.</p><script>alert('xss & stuff');</script><style>.x { color: red; }</style><p>After.</p>`
	got := ArcDescriptionPlainText(html, "")
	if strings.Contains(got, "alert") || strings.Contains(got, "xss") {
		t.Fatalf("expected <script> content dropped, got %q", got)
	}
	if strings.Contains(got, "color: red") || strings.Contains(got, ".x {") {
		t.Fatalf("expected <style> content dropped, got %q", got)
	}
	if !strings.Contains(got, "Before.") || !strings.Contains(got, "After.") {
		t.Fatalf("expected surrounding text preserved, got %q", got)
	}
}

func TestArcDescriptionPlainTextTruncatesUnclosedTag(t *testing.T) {
	html := `<p>Full text.</p><div class="broken`
	got := ArcDescriptionPlainText(html, "")
	if strings.Contains(got, "<") || strings.Contains(got, "broken") {
		t.Fatalf("expected unclosed tag fragment truncated, got %q", got)
	}
	if !strings.Contains(got, "Full text.") {
		t.Fatalf("expected preceding text preserved, got %q", got)
	}
}

func TestArcDescriptionPlainTextCapsAtWordBoundary(t *testing.T) {
	long := "<p>" + strings.Repeat("word ", 500) + "</p>" // 2500 chars, well over the cap
	got := ArcDescriptionPlainText(long, "")
	if len(got) > 2000 {
		t.Fatalf("expected cap at 2000 chars, got %d", len(got))
	}
	if strings.HasSuffix(got, "wor") || strings.HasSuffix(got, "wo") {
		t.Fatalf("expected cap on a word boundary, got tail %q", got[len(got)-10:])
	}
}
