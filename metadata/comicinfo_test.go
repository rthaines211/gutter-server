package metadata

import (
	"strings"
	"testing"
)

func TestParseComicInfo(t *testing.T) {
	xml := []byte(`<?xml version="1.0" encoding="utf-8"?>
<ComicInfo>
  <Title>Test Issue</Title>
  <Series>Test Series</Series>
  <Number>1</Number>
  <Writer>Test Writer</Writer>
  <Penciller>Test Artist</Penciller>
  <Summary>A test comic.</Summary>
  <PageCount>3</PageCount>
</ComicInfo>`)

	info, err := ParseComicInfo(xml)
	if err != nil {
		t.Fatalf("ParseComicInfo failed: %v", err)
	}
	if info.Title != "Test Issue" {
		t.Errorf("expected title 'Test Issue', got %q", info.Title)
	}
	if info.Series != "Test Series" {
		t.Errorf("expected series 'Test Series', got %q", info.Series)
	}
	if info.Number != "1" {
		t.Errorf("expected number '1', got %q", info.Number)
	}
	if info.Writer != "Test Writer" {
		t.Errorf("expected writer 'Test Writer', got %q", info.Writer)
	}
	if info.Penciller != "Test Artist" {
		t.Errorf("expected penciller 'Test Artist', got %q", info.Penciller)
	}
	if info.Summary != "A test comic." {
		t.Errorf("expected summary 'A test comic.', got %q", info.Summary)
	}
	if info.PageCount != 3 {
		t.Errorf("expected page count 3, got %d", info.PageCount)
	}
}

func TestParseComicInfoNil(t *testing.T) {
	info, err := ParseComicInfo(nil)
	if err != nil {
		t.Fatalf("ParseComicInfo(nil) should not error: %v", err)
	}
	if info.Title != "" {
		t.Error("expected empty info for nil input")
	}
}

func TestParseComicInfoPartial(t *testing.T) {
	xml := []byte(`<ComicInfo><Title>Only Title</Title></ComicInfo>`)
	info, err := ParseComicInfo(xml)
	if err != nil {
		t.Fatal(err)
	}
	if info.Title != "Only Title" {
		t.Errorf("expected 'Only Title', got %q", info.Title)
	}
	if info.Series != "" {
		t.Errorf("expected empty series, got %q", info.Series)
	}
}

func TestParseComicInfoFullFields(t *testing.T) {
	xml := []byte(`<?xml version="1.0"?>
<ComicInfo>
  <Title>Batman #1</Title>
  <Series>Batman</Series>
  <Number>1</Number>
  <Writer>Tom King</Writer>
  <Penciller>David Finch</Penciller>
  <Publisher>DC Comics</Publisher>
  <Genre>Superhero, Action, Crime</Genre>
  <Year>2016</Year>
  <Summary>A new era begins.</Summary>
  <PageCount>24</PageCount>
</ComicInfo>`)

	info, err := ParseComicInfo(xml)
	if err != nil {
		t.Fatal(err)
	}
	if info.Publisher != "DC Comics" {
		t.Errorf("publisher: got %q, want %q", info.Publisher, "DC Comics")
	}
	if info.Genre != "Superhero, Action, Crime" {
		t.Errorf("genre: got %q, want %q", info.Genre, "Superhero, Action, Crime")
	}
	if info.Year != 2016 {
		t.Errorf("year: got %d, want %d", info.Year, 2016)
	}
}

func TestParseComicInfoMissingNewFields(t *testing.T) {
	xml := []byte(`<?xml version="1.0"?>
<ComicInfo>
  <Title>Old Comic</Title>
  <Writer>Frank Miller</Writer>
</ComicInfo>`)

	info, err := ParseComicInfo(xml)
	if err != nil {
		t.Fatal(err)
	}
	if info.Publisher != "" {
		t.Errorf("publisher: got %q, want empty", info.Publisher)
	}
	if info.Genre != "" {
		t.Errorf("genre: got %q, want empty", info.Genre)
	}
	if info.Year != 0 {
		t.Errorf("year: got %d, want 0", info.Year)
	}
}

func TestParseComicInfoNilData(t *testing.T) {
	info, err := ParseComicInfo(nil)
	if err != nil {
		t.Fatal(err)
	}
	if info.Publisher != "" || info.Genre != "" || info.Year != 0 {
		t.Errorf("expected zero values for nil input, got %+v", info)
	}
}

func TestParseComicInfoChapters(t *testing.T) {
	xml := []byte(`<?xml version="1.0"?>
<ComicInfo>
  <Title>Collected</Title>
  <PageCount>10</PageCount>
  <Pages>
    <Page Image="0" Bookmark="Chapter One"/>
    <Page Image="1"/>
    <Page Image="4" Bookmark="Chapter Two"/>
  </Pages>
</ComicInfo>`)
	info, err := ParseComicInfo(xml)
	if err != nil {
		t.Fatal(err)
	}
	if len(info.Chapters) != 2 {
		t.Fatalf("expected 2 chapters, got %d", len(info.Chapters))
	}
	if info.Chapters[0].Page != 0 || info.Chapters[0].Title != "Chapter One" {
		t.Errorf("chapter 0: got %+v", info.Chapters[0])
	}
	if info.Chapters[1].Page != 4 || info.Chapters[1].Title != "Chapter Two" {
		t.Errorf("chapter 1: got %+v", info.Chapters[1])
	}
}

func TestParseComicInfoNoChapters(t *testing.T) {
	xml := []byte(`<ComicInfo><Title>Floppy</Title><PageCount>22</PageCount></ComicInfo>`)
	info, err := ParseComicInfo(xml)
	if err != nil {
		t.Fatal(err)
	}
	if len(info.Chapters) != 0 {
		t.Errorf("expected no chapters, got %d", len(info.Chapters))
	}
}

func TestMarshalComicInfoRoundTrip(t *testing.T) {
	in := &ComicInfo{
		Title:     "The Test",
		Series:    "Test Series",
		Number:    "3",
		Writer:    "A. Writer",
		Publisher: "Test House",
		Year:      2020,
		Summary:   "A summary.",
		Format:    "TPB",
		Volume:    "2",
	}
	data, err := MarshalComicInfo(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.HasPrefix(string(data), "<?xml") {
		t.Fatalf("missing xml header: %q", data[:20])
	}
	out, err := ParseComicInfo(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if out.Title != in.Title || out.Number != in.Number || out.Format != "TPB" || out.Volume != "2" || out.Year != 2020 {
		t.Fatalf("round trip mismatch: %+v", out)
	}
}

func TestComicInfoRoundTripPreservesUnmodeledElements(t *testing.T) {
	xmlIn := []byte(`<?xml version="1.0" encoding="utf-8"?>
<ComicInfo>
  <Title>Batman #1</Title>
  <Series>Batman</Series>
  <Notes>keep me</Notes>
  <Web>http://x</Web>
  <Characters>Batman</Characters>
</ComicInfo>`)

	info, err := ParseComicInfo(xmlIn)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	data, err := MarshalComicInfo(info)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	out := string(data)
	if !strings.Contains(out, "<Notes>keep me</Notes>") {
		t.Errorf("Notes not preserved, got: %s", out)
	}
	if !strings.Contains(out, "<Web>http://x</Web>") {
		t.Errorf("Web not preserved, got: %s", out)
	}
	if !strings.Contains(out, "<Characters>Batman</Characters>") {
		t.Errorf("Characters not preserved, got: %s", out)
	}
}

func TestComicInfoRoundTripNoDuplicateModeledFields(t *testing.T) {
	xmlIn := []byte(`<ComicInfo><Title>Batman #1</Title><Series>Batman</Series><Notes>hi</Notes></ComicInfo>`)
	info, err := ParseComicInfo(xmlIn)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	data, err := MarshalComicInfo(info)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	out := string(data)
	if n := strings.Count(out, "<Title>"); n != 1 {
		t.Errorf("expected exactly one <Title>, got %d: %s", n, out)
	}
	if n := strings.Count(out, "<Series>"); n != 1 {
		t.Errorf("expected exactly one <Series>, got %d: %s", n, out)
	}
}

func TestComicInfoRoundTripPagesNotDuplicated(t *testing.T) {
	xmlIn := []byte(`<ComicInfo><Title>Collected</Title><PageCount>10</PageCount><Pages><Page Image="0" Bookmark="Chapter One"/><Page Image="1"/></Pages></ComicInfo>`)
	info, err := ParseComicInfo(xmlIn)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	data, err := MarshalComicInfo(info)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	out := string(data)
	if n := strings.Count(out, "<Pages>"); n != 1 {
		t.Errorf("expected exactly one <Pages> container, got %d: %s", n, out)
	}
	roundTripped, err := ParseComicInfo(data)
	if err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	if len(roundTripped.Pages) != 2 {
		t.Fatalf("expected 2 pages, got %d: %+v", len(roundTripped.Pages), roundTripped.Pages)
	}
	if roundTripped.Pages[0].Bookmark != "Chapter One" {
		t.Errorf("page 0 bookmark: got %q", roundTripped.Pages[0].Bookmark)
	}
}

func TestComicInfoRoundTripCreditFields(t *testing.T) {
	in := &ComicInfo{
		Series:      "Batman",
		Number:      "1",
		Writer:      "Tom King",
		Penciller:   "David Finch",
		Inker:       "Danny Miki",
		Colorist:    "Jordie Bellaire",
		Letterer:    "John Workman",
		CoverArtist: "David Finch, Matt Banning",
		Editor:      "Jamie Rich",
		Year:        2016,
		Month:       8,
		Day:         3,
	}
	data, err := MarshalComicInfo(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got, err := ParseComicInfo(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got.Inker != "Danny Miki" || got.Colorist != "Jordie Bellaire" ||
		got.Letterer != "John Workman" || got.CoverArtist != "David Finch, Matt Banning" ||
		got.Editor != "Jamie Rich" {
		t.Errorf("credit fields not round-tripped: %+v", got)
	}
	if got.Month != 8 || got.Day != 3 {
		t.Errorf("date fields not round-tripped: month=%d day=%d", got.Month, got.Day)
	}
}
