// Package opds builds OPDS 1.2 catalog documents (Atom feeds) with the
// OPDS-PSE page-streaming extension. It knows nothing about the Gutter store
// or HTTP: callers fill in Feed/Entry values and call Marshal.
package opds

import (
	"bytes"
	"encoding/xml"
	"strconv"
	"strings"
	"time"
)

// Media types used in OPDS links.
const (
	TypeNavigation  = "application/atom+xml;profile=opds-catalog;kind=navigation"
	TypeAcquisition = "application/atom+xml;profile=opds-catalog;kind=acquisition"
	TypeOpenSearch  = "application/opensearchdescription+xml"
	TypeCBZ         = "application/vnd.comicbook+zip"
	TypeCBR         = "application/vnd.comicbook-rar"
	TypeJPEG        = "image/jpeg"

	// ContentType is what feed responses are served as.
	ContentType = "application/atom+xml;charset=utf-8"
	// OpenSearchContentType is what the OpenSearch description is served as.
	OpenSearchContentType = "application/opensearchdescription+xml;charset=utf-8"
)

// Link relations.
const (
	RelSelf        = "self"
	RelStart       = "start"
	RelUp          = "up"
	RelNext        = "next"
	RelPrevious    = "previous"
	RelSearch      = "search"
	RelSubsection  = "subsection"
	RelAcquisition = "http://opds-spec.org/acquisition"
	RelImage       = "http://opds-spec.org/image"
	RelThumbnail   = "http://opds-spec.org/image/thumbnail"
	RelPSEStream   = "http://vaemendis.net/opds-pse/stream"
	RelSortNew     = "http://opds-spec.org/sort/new"
)

// Namespaces declared on every feed.
const (
	nsAtom       = "http://www.w3.org/2005/Atom"
	nsOPDS       = "http://opds-spec.org/2010/catalog"
	nsPSE        = "http://vaemendis.net/opds-pse/ns"
	nsOpenSearch = "http://a9.com/-/spec/opensearch/1.1/"
	nsDC         = "http://purl.org/dc/terms/"
)

// PageNumberTemplate is the OPDS-PSE placeholder in a stream link href.
// Clients substitute a 0-based page index.
const PageNumberTemplate = "{pageNumber}"

// Feed is an OPDS catalog feed (navigation or acquisition).
type Feed struct {
	ID      string
	Title   string
	Updated time.Time
	Links   []Link
	Entries []Entry
	// TotalResults/ItemsPerPage/StartIndex emit OpenSearch paging hints when
	// ItemsPerPage > 0.
	TotalResults int
	ItemsPerPage int
	StartIndex   int
}

// Entry is one catalog entry: a navigation entry (Content + subsection link)
// or a publication (acquisition + image links).
type Entry struct {
	ID       string
	Title    string
	Updated  time.Time
	Authors  []string
	Summary  string
	Content  string // plain text
	Issued   string // dc:issued, e.g. "2016" or "2016-06-01"
	Language string
	Links    []Link
}

// Link is an Atom link. PSE* fields are only emitted on PSE stream links.
type Link struct {
	Rel   string
	Href  string
	Type  string
	Title string

	PSECount        int       // pse:count (emitted when > 0)
	PSELastRead     *int      // pse:lastRead (0-based), nil = unread
	PSELastReadDate time.Time // pse:lastReadDate, zero = omitted
}

// StreamLink builds an OPDS-PSE stream link. hrefTemplate must contain
// PageNumberTemplate; lastRead is the 0-based last page read (nil if unread).
func StreamLink(hrefTemplate string, count int, lastRead *int, lastReadDate time.Time) Link {
	return Link{
		Rel:             RelPSEStream,
		Href:            hrefTemplate,
		Type:            TypeJPEG,
		PSECount:        count,
		PSELastRead:     lastRead,
		PSELastReadDate: lastReadDate,
	}
}

// --- XML wire types ---------------------------------------------------------

type xmlFeed struct {
	XMLName      xml.Name   `xml:"feed"`
	Xmlns        string     `xml:"xmlns,attr"`
	XmlnsOPDS    string     `xml:"xmlns:opds,attr"`
	XmlnsPSE     string     `xml:"xmlns:pse,attr"`
	XmlnsOS      string     `xml:"xmlns:opensearch,attr"`
	XmlnsDC      string     `xml:"xmlns:dc,attr"`
	ID           string     `xml:"id"`
	Title        string     `xml:"title"`
	Updated      string     `xml:"updated"`
	Author       xmlAuthor  `xml:"author"`
	TotalResults string     `xml:"opensearch:totalResults,omitempty"`
	ItemsPerPage string     `xml:"opensearch:itemsPerPage,omitempty"`
	StartIndex   string     `xml:"opensearch:startIndex,omitempty"`
	Links        []xmlLink  `xml:"link"`
	Entries      []xmlEntry `xml:"entry"`
}

type xmlAuthor struct {
	Name string `xml:"name"`
	URI  string `xml:"uri,omitempty"`
}

type xmlText struct {
	Type string `xml:"type,attr"`
	Text string `xml:",chardata"`
}

type xmlEntry struct {
	ID       string      `xml:"id"`
	Title    string      `xml:"title"`
	Updated  string      `xml:"updated"`
	Authors  []xmlAuthor `xml:"author"`
	Issued   string      `xml:"dc:issued,omitempty"`
	Language string      `xml:"dc:language,omitempty"`
	Summary  *xmlText    `xml:"summary,omitempty"`
	Content  *xmlText    `xml:"content,omitempty"`
	Links    []xmlLink   `xml:"link"`
}

type xmlLink struct {
	Rel          string `xml:"rel,attr,omitempty"`
	Href         string `xml:"href,attr"`
	Type         string `xml:"type,attr,omitempty"`
	Title        string `xml:"title,attr,omitempty"`
	Count        string `xml:"pse:count,attr,omitempty"`
	LastRead     string `xml:"pse:lastRead,attr,omitempty"`
	LastReadDate string `xml:"pse:lastReadDate,attr,omitempty"`
}

func atomTime(t time.Time) string {
	if t.IsZero() {
		t = time.Unix(0, 0)
	}
	return t.UTC().Format(time.RFC3339)
}

func toXMLLink(l Link) xmlLink {
	x := xmlLink{Rel: l.Rel, Href: l.Href, Type: l.Type, Title: l.Title}
	if l.PSECount > 0 {
		x.Count = strconv.Itoa(l.PSECount)
	}
	if l.PSELastRead != nil {
		x.LastRead = strconv.Itoa(*l.PSELastRead)
	}
	if !l.PSELastReadDate.IsZero() {
		x.LastReadDate = atomTime(l.PSELastReadDate)
	}
	return x
}

func toXMLLinks(ls []Link) []xmlLink {
	out := make([]xmlLink, len(ls))
	for i, l := range ls {
		out[i] = toXMLLink(l)
	}
	return out
}

// Marshal renders the feed as an XML document (with declaration).
func (f Feed) Marshal() ([]byte, error) {
	xf := xmlFeed{
		Xmlns:     nsAtom,
		XmlnsOPDS: nsOPDS,
		XmlnsPSE:  nsPSE,
		XmlnsOS:   nsOpenSearch,
		XmlnsDC:   nsDC,
		ID:        f.ID,
		Title:     f.Title,
		Updated:   atomTime(f.Updated),
		Author:    xmlAuthor{Name: "Gutter"},
		Links:     toXMLLinks(f.Links),
		Entries:   make([]xmlEntry, 0, len(f.Entries)),
	}
	if f.ItemsPerPage > 0 {
		xf.TotalResults = strconv.Itoa(f.TotalResults)
		xf.ItemsPerPage = strconv.Itoa(f.ItemsPerPage)
		xf.StartIndex = strconv.Itoa(f.StartIndex)
	}
	for _, e := range f.Entries {
		xe := xmlEntry{
			ID:       e.ID,
			Title:    e.Title,
			Updated:  atomTime(e.Updated),
			Issued:   e.Issued,
			Language: e.Language,
			Links:    toXMLLinks(e.Links),
		}
		for _, a := range e.Authors {
			if a != "" {
				xe.Authors = append(xe.Authors, xmlAuthor{Name: a})
			}
		}
		if e.Summary != "" {
			xe.Summary = &xmlText{Type: "text", Text: e.Summary}
		}
		if e.Content != "" {
			xe.Content = &xmlText{Type: "text", Text: e.Content}
		}
		xf.Entries = append(xf.Entries, xe)
	}
	var buf bytes.Buffer
	buf.WriteString(xml.Header)
	enc := xml.NewEncoder(&buf)
	enc.Indent("", "  ")
	if err := enc.Encode(xf); err != nil {
		return nil, err
	}
	buf.WriteByte('\n')
	return buf.Bytes(), nil
}

// --- OpenSearch description ---------------------------------------------------

type xmlOpenSearch struct {
	XMLName        xml.Name   `xml:"OpenSearchDescription"`
	Xmlns          string     `xml:"xmlns,attr"`
	ShortName      string     `xml:"ShortName"`
	Description    string     `xml:"Description"`
	InputEncoding  string     `xml:"InputEncoding"`
	OutputEncoding string     `xml:"OutputEncoding"`
	URLs           []xmlOSURL `xml:"Url"`
}

type xmlOSURL struct {
	Type     string `xml:"type,attr"`
	Template string `xml:"template,attr"`
}

// SearchTermsTemplate is the OpenSearch placeholder in a search template.
const SearchTermsTemplate = "{searchTerms}"

// OpenSearchDescription renders an OpenSearch 1.1 description whose single
// Url points at template (which must contain SearchTermsTemplate) and returns
// results of resultType.
func OpenSearchDescription(shortName, description, template, resultType string) ([]byte, error) {
	d := xmlOpenSearch{
		Xmlns:          nsOpenSearch,
		ShortName:      shortName,
		Description:    description,
		InputEncoding:  "UTF-8",
		OutputEncoding: "UTF-8",
		URLs:           []xmlOSURL{{Type: resultType, Template: template}},
	}
	var buf bytes.Buffer
	buf.WriteString(xml.Header)
	enc := xml.NewEncoder(&buf)
	enc.Indent("", "  ")
	if err := enc.Encode(d); err != nil {
		return nil, err
	}
	buf.WriteByte('\n')
	return buf.Bytes(), nil
}

// MimeForExt returns the acquisition media type for a comic file extension
// (with or without the dot, any case). Unknown extensions get
// application/octet-stream.
func MimeForExt(ext string) string {
	switch strings.ToLower(strings.TrimPrefix(ext, ".")) {
	case "cbz", "zip":
		return TypeCBZ
	case "cbr", "rar":
		return TypeCBR
	default:
		return "application/octet-stream"
	}
}
