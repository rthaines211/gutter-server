package opds

import (
	"encoding/xml"
	"strings"
	"testing"
	"time"
)

func intp(i int) *int { return &i }

func TestFeedMarshal(t *testing.T) {
	ts := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		feed    Feed
		want    []string // substrings that must appear
		notWant []string // substrings that must not appear
	}{
		{
			name: "navigation root",
			feed: Feed{
				ID: "urn:gutter:root", Title: "Gutter", Updated: ts,
				Links: []Link{
					{Rel: RelSelf, Href: "/opds/", Type: TypeNavigation},
					{Rel: RelSearch, Href: "/opds/opensearch.xml", Type: TypeOpenSearch},
				},
				Entries: []Entry{{
					ID: "urn:gutter:series", Title: "All series", Updated: ts, Content: "Browse",
					Links: []Link{{Rel: RelSubsection, Href: "/opds/series", Type: TypeNavigation}},
				}},
			},
			want: []string{
				`<?xml version="1.0" encoding="UTF-8"?>`,
				`<feed xmlns="http://www.w3.org/2005/Atom"`,
				`xmlns:pse="http://vaemendis.net/opds-pse/ns"`,
				`<id>urn:gutter:root</id>`,
				`<updated>2026-11-08T12:00:00Z</updated>`,
				`<link rel="self" href="/opds/" type="application/atom+xml;profile=opds-catalog;kind=navigation">`,
				`<link rel="search" href="/opds/opensearch.xml" type="application/opensearchdescription+xml">`,
				`<content type="text">Browse</content>`,
				`<link rel="subsection" href="/opds/series"`,
			},
			notWant: []string{"opensearch:totalResults", "pse:count", "<summary"},
		},
		{
			name: "acquisition entry with PSE stream",
			feed: Feed{
				ID: "urn:gutter:series:x", Title: "Saga", Updated: ts,
				Entries: []Entry{{
					ID: "urn:gutter:comic:abc", Title: "Saga #1", Updated: ts,
					Authors: []string{"Brian K. Vaughan", ""}, Summary: "A & B <c>", Issued: "2012",
					Links: []Link{
						{Rel: RelAcquisition, Href: "/opds/download/abc", Type: TypeCBZ},
						{Rel: RelImage, Href: "/comics/abc/cover", Type: TypeJPEG},
						{Rel: RelThumbnail, Href: "/comics/abc/cover", Type: TypeJPEG},
						StreamLink("/comics/abc/pages/"+PageNumberTemplate, 24, intp(5), ts),
					},
				}},
			},
			want: []string{
				`<author>`, `<name>Brian K. Vaughan</name>`,
				`<summary type="text">A &amp; B &lt;c&gt;</summary>`,
				`<dc:issued>2012</dc:issued>`,
				`rel="http://opds-spec.org/acquisition" href="/opds/download/abc" type="application/vnd.comicbook+zip"`,
				`rel="http://opds-spec.org/image/thumbnail"`,
				`rel="http://vaemendis.net/opds-pse/stream" href="/comics/abc/pages/{pageNumber}" type="image/jpeg" pse:count="24" pse:lastRead="5" pse:lastReadDate="2026-11-08T12:00:00Z"`,
			},
			notWant: []string{"<name></name>", "%7BpageNumber%7D"},
		},
		{
			name: "unread stream link omits lastRead",
			feed: Feed{
				ID: "f", Title: "t",
				Entries: []Entry{{ID: "e", Title: "e", Links: []Link{
					StreamLink("/p/{pageNumber}", 3, nil, time.Time{}),
				}}},
			},
			want:    []string{`pse:count="3"`, `<updated>1970-01-01T00:00:00Z</updated>`},
			notWant: []string{"pse:lastRead"},
		},
		{
			name: "lastRead zero is emitted",
			feed: Feed{ID: "f", Title: "t", Entries: []Entry{{ID: "e", Title: "e", Links: []Link{
				StreamLink("/p/{pageNumber}", 3, intp(0), time.Time{}),
			}}}},
			want: []string{`pse:lastRead="0"`},
		},
		{
			name: "paging hints",
			feed: Feed{ID: "f", Title: "t", TotalResults: 250, ItemsPerPage: 100, StartIndex: 101,
				Links: []Link{{Rel: RelNext, Href: "/opds/series?page=3", Type: TypeNavigation}}},
			want: []string{
				`<opensearch:totalResults>250</opensearch:totalResults>`,
				`<opensearch:itemsPerPage>100</opensearch:itemsPerPage>`,
				`<opensearch:startIndex>101</opensearch:startIndex>`,
				`rel="next" href="/opds/series?page=3"`,
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, err := tc.feed.Marshal()
			if err != nil {
				t.Fatal(err)
			}
			s := string(out)
			for _, w := range tc.want {
				if !strings.Contains(s, w) {
					t.Errorf("missing %q in:\n%s", w, s)
				}
			}
			for _, w := range tc.notWant {
				if strings.Contains(s, w) {
					t.Errorf("unexpected %q in:\n%s", w, s)
				}
			}
			// Must be well-formed XML.
			var v struct {
				XMLName xml.Name
			}
			if err := xml.Unmarshal(out, &v); err != nil {
				t.Fatalf("not well-formed: %v", err)
			}
			if v.XMLName.Local != "feed" || v.XMLName.Space != nsAtom {
				t.Errorf("root = %+v", v.XMLName)
			}
		})
	}
}

func TestOpenSearchDescription(t *testing.T) {
	out, err := OpenSearchDescription("Gutter", "Search", "/opds/search?q="+SearchTermsTemplate, TypeNavigation)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, w := range []string{
		`<OpenSearchDescription xmlns="http://a9.com/-/spec/opensearch/1.1/">`,
		`<ShortName>Gutter</ShortName>`,
		`template="/opds/search?q={searchTerms}"`,
	} {
		if !strings.Contains(s, w) {
			t.Errorf("missing %q in:\n%s", w, s)
		}
	}
}

func TestMimeForExt(t *testing.T) {
	tests := map[string]string{
		".cbz": TypeCBZ, "CBZ": TypeCBZ, ".cbr": TypeCBR, ".CBR": TypeCBR,
		".pdf": "application/octet-stream", "": "application/octet-stream",
	}
	for in, want := range tests {
		if got := MimeForExt(in); got != want {
			t.Errorf("MimeForExt(%q) = %q, want %q", in, got, want)
		}
	}
}
