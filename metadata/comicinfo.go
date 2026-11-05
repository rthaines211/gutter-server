package metadata

import "encoding/xml"

type ComicPage struct {
	Image    int    `xml:"Image,attr"`
	Bookmark string `xml:"Bookmark,attr"`
}

type Chapter struct {
	Page  int    `json:"page"`
	Title string `json:"title"`
}

// UnknownElement captures a ComicInfo child element this struct does not
// model, preserving its raw markup (including any attributes/children) so
// it can be re-emitted verbatim on marshal.
type UnknownElement struct {
	XMLName xml.Name
	Attrs   []xml.Attr `xml:",any,attr"`
	Content []byte     `xml:",innerxml"`
}

type ComicInfo struct {
	Title       string      `xml:"Title" json:"title"`
	Series      string      `xml:"Series" json:"series"`
	Number      string      `xml:"Number" json:"number"`
	Format      string      `xml:"Format,omitempty"`
	Volume      string      `xml:"Volume,omitempty"`
	Writer      string      `xml:"Writer" json:"writer"`
	Penciller   string      `xml:"Penciller" json:"penciller"`
	Inker       string      `xml:"Inker,omitempty" json:"inker,omitempty"`
	Colorist    string      `xml:"Colorist,omitempty" json:"colorist,omitempty"`
	Letterer    string      `xml:"Letterer,omitempty" json:"letterer,omitempty"`
	CoverArtist string      `xml:"CoverArtist,omitempty" json:"cover_artist,omitempty"`
	Editor      string      `xml:"Editor,omitempty" json:"editor,omitempty"`
	Publisher   string      `xml:"Publisher" json:"publisher"`
	Genre       string      `xml:"Genre" json:"genre"`
	Year        int         `xml:"Year" json:"year"`
	Month       int         `xml:"Month,omitempty" json:"month,omitempty"`
	Day         int         `xml:"Day,omitempty" json:"day,omitempty"`
	Summary     string      `xml:"Summary" json:"summary"`
	PageCount   int         `xml:"PageCount" json:"page_count"`
	Pages       []ComicPage `xml:"Pages>Page" json:"-"`
	Chapters    []Chapter   `xml:"-" json:"chapters,omitempty"`

	// Unknown captures any ComicInfo child element this struct does not model
	// (Notes, Web, Characters, Teams, StoryArc, AgeRating, LanguageISO, Count,
	// AlternateSeries, Imprint, ScanInformation, …) so writeback preserves
	// them instead of dropping them on rewrite. encoding/xml only routes an
	// element into a ",any" field when no other field claims it first, so
	// modeled fields (including <Pages>, claimed by the "Pages>Page" path
	// above) are never duplicated into Unknown — verified by
	// TestComicInfoRoundTripNoDuplicateModeledFields and
	// TestComicInfoRoundTripPagesNotDuplicated.
	Unknown []UnknownElement `xml:",any" json:"-"`
}

func ParseComicInfo(data []byte) (*ComicInfo, error) {
	if data == nil || len(data) == 0 {
		return &ComicInfo{}, nil
	}
	var info ComicInfo
	if err := xml.Unmarshal(data, &info); err != nil {
		return nil, err
	}
	for _, p := range info.Pages {
		if p.Bookmark != "" {
			info.Chapters = append(info.Chapters, Chapter{Page: p.Image, Title: p.Bookmark})
		}
	}
	return &info, nil
}

// MarshalComicInfo renders a ComicInfo as a standalone XML document
// suitable for writing into an archive as ComicInfo.xml.
func MarshalComicInfo(ci *ComicInfo) ([]byte, error) {
	body, err := xml.MarshalIndent(ci, "", "  ")
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(xml.Header)+len(body)+1)
	out = append(out, []byte(xml.Header)...)
	out = append(out, body...)
	out = append(out, '\n')
	return out, nil
}
