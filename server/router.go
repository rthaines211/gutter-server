package server

import (
	"encoding/base64"
	"io"
	"net/http"
	"sync"
	"sync/atomic"

	"github.com/rthaines211/gutter-server/cache"
	"github.com/rthaines211/gutter-server/creators"
	"github.com/rthaines211/gutter-server/enrichment"
	"github.com/rthaines211/gutter-server/notify"
	"github.com/rthaines211/gutter-server/ocr"
	"github.com/rthaines211/gutter-server/server/admin"
	"github.com/rthaines211/gutter-server/store"
)

type Server struct {
	ComicDirs  []string
	Store      *store.Store
	Cache      *cache.LRU
	CoverDir   string
	BubblesDir string // bubble-zoom sidecars written by hostjob/bubbles; "" disables
	Admin      *admin.Admin
	Rescan     func() // scan + pipeline; HandleRefreshLibrary uses it when set
	Enrichment *enrichment.Worker
	Creators   *creators.Index // nil when localcv isn't configured
	ocrRunner  ocr.PageOCR
	detectOnce sync.Once
	detectReg  *detectRegistry
	remoteOnce sync.Once
	remoteHubV *remoteHub

	// PushSecret gates /devices; "" means push is off and they 404.
	PushSecret string

	// Password (Basic) and APIKey (Bearer) gate every endpoint but /healthz;
	// AuthOff (GUTTER_AUTH=off) disables the gate.
	Password string
	APIKey   string
	AuthOff  bool
	Notifier *notify.Notifier

	// lastPageServed is the unix-nano time the reader last served a comic page;
	// the tagger worker yields while this is recent.
	lastPageServed atomic.Int64

	// servedComics maps comic ID -> unix-nano time last served (page or
	// download), for the enrichment app's active-file check.
	servedComics sync.Map
}

// jobs returns the detect-job registry, lazily initialized so test servers
// built as struct literals work without calling NewServer.
func (s *Server) jobs() *detectRegistry {
	s.detectOnce.Do(func() { s.detectReg = newDetectRegistry() })
	return s.detectReg
}

// hub returns the remote-control hub, lazily initialized so test servers
// built as struct literals work without calling NewServer.
func (s *Server) hub() *remoteHub {
	s.remoteOnce.Do(func() { s.remoteHubV = newRemoteHub() })
	return s.remoteHubV
}

// NewServer constructs a Server with production defaults.
func NewServer(comicDirs []string, store *store.Store, lru *cache.LRU) *Server {
	return &Server{
		ComicDirs: comicDirs,
		Store:     store,
		Cache:     lru,
		ocrRunner: func(_ int, img io.Reader) (string, error) {
			return ocr.RunTesseract(img)
		},
	}
}

type route struct {
	pattern string
	handler http.HandlerFunc
}

// routes is the single source of truth for API registration; the registry
// guard test diffs it against RouteRegistry.
func (s *Server) routes() []route {
	return []route{
		{"GET /healthz", s.HandleHealthz},
		{"GET /browse", s.HandleBrowse},
		// {"GET /search", s.HandleSearch}, // TODO: not yet implemented
		{"GET /comics/{id}/download", s.HandleDownloadComic},
		{"GET /comics/{id}/info", s.HandleComicInfo},
		{"POST /comics/{id}/detect-chapters", s.HandleDetectChapters},
		{"GET /comics/{id}/detect-chapters/status", s.HandleDetectChaptersStatus},
		{"GET /comics/{id}/pages/{num}", s.HandleComicPage},
		{"GET /comics/{id}/pages/{num}/bubbles", s.HandlePageBubbles},
		{"PUT /comics/{id}/pages/{num}/bubbles/order", s.HandlePutBubbleOrder},
		{"PUT /comics/{id}/pages/{num}/flag", s.HandleFlagPage},
		{"GET /page-flags", s.HandleListPageFlags},
		{"GET /comics/{id}/bubbles", s.HandleComicBubbles},
		{"GET /comics/{id}/cover", s.HandleComicCover},
		{"PUT /comics/{id}/rename", s.HandleRenameComic},
		{"PATCH /comics/{id}/metadata", s.HandlePatchComicMetadata},
		{"PATCH /series/{id}/metadata", s.HandlePatchSeriesMetadata},
		{"GET /reading", s.HandleGetReading},
		{"PUT /reading/batch", s.HandleBatchReading},
		{"DELETE /reading/batch", s.HandleBatchDeleteReading},
		{"PUT /reading/{id}", s.HandlePutReading},
		{"GET /sync", s.HandleSync},
		{"GET /settings", s.HandleGetSettings},
		{"PUT /settings", s.HandlePutSettings},
		{"GET /facets/{facet}", s.HandleListFacets},
		{"GET /library/{id}/cover", s.HandleSeriesCover},
		{"GET /library/{id}/recap", s.HandleLibraryRecap},
		{"GET /library/refresh/status", s.HandleScanStatus},
		{"GET /library/{id}", s.HandleGetSeries},
		{"GET /library", s.HandleListLibrary},
		{"POST /library/refresh", s.HandleRefreshLibrary},
		{"GET /collections", s.HandleListCollections},
		{"POST /collections", s.HandleCreateCollection},
		{"PUT /collections/{id}", s.HandleUpdateCollection},
		{"DELETE /collections/{id}", s.HandleDeleteCollection},
		{"POST /collections/{id}/series", s.HandleAddSeriesToCollection},
		{"DELETE /collections/{id}/series/{seriesId}", s.HandleRemoveSeriesFromCollection},
		{"PUT /collections/{id}/series", s.HandleReorderCollectionSeries},
		{"GET /queues", s.HandleListQueues},
		{"PUT /queues/{id}", s.HandlePutQueue},
		{"DELETE /queues/{id}", s.HandleDeleteQueue},
		// Smart filters
		{"GET /smart-filters", s.HandleListSmartFilters},
		{"POST /smart-filters", s.HandleCreateSmartFilter},
		{"GET /smart-filters/{id}/series", s.HandleSmartFilterSeries},
		{"PUT /smart-filters/{id}", s.HandleUpdateSmartFilter},
		{"DELETE /smart-filters/{id}", s.HandleDeleteSmartFilter},
		{"GET /presets", s.HandleListPresets},
		{"POST /presets", s.HandleCreatePreset},
		{"PUT /presets/{id}", s.HandleUpdatePreset},
		{"DELETE /presets/{id}", s.HandleDeletePreset},
		{"POST /presets/{id}/reset", s.HandleResetPreset},
		// Stats & reading sessions
		{"POST /reading/sessions", s.HandleCreateSession},
		{"GET /stats/summary", s.HandleStatsSummary},
		{"GET /stats/history", s.HandleStatsHistory},
		// Rename templates
		{"GET /rename-templates", s.HandleGetRenameTemplates},
		{"PUT /rename-templates", s.HandlePutRenameTemplates},
		{"POST /series/{id}/apply-template", s.HandleApplyTemplate},
		{"PUT /series/{id}/rename-folder", s.HandleRenameFolder},
		// Enrichment
		{"GET /enrichment/status", s.HandleEnrichmentStatus},
		{"GET /enrichment/pending", s.HandleEnrichmentPending},
		{"POST /enrichment/{id}/match", s.HandleEnrichmentMatch},
		{"POST /enrichment/{id}/requeue", s.HandleEnrichmentRequeue},
		// Integration (external tools, e.g. the comic-enrichment app)
		{"POST /integration/file-move", s.HandleFileMove},
		{"GET /integration/file-state", s.HandleFileState},
		// Remote control (iPhone companion)
		{"GET /remote/events", s.HandleRemoteEvents},
		{"POST /remote/command", s.HandleRemoteCommand},
		{"POST /remote/state", s.HandleRemoteState},
		// Push notifications (iOS app device tokens)
		{"POST /devices", s.HandleRegisterDevice},
		{"DELETE /devices/{token}", s.HandleDeleteDevice},
		// Creators (writer/artist/cover-artist pages)
		{"GET /creators/lookup", s.HandleCreatorLookup},
		{"GET /creators/volumes/{id}", s.HandleCreatorVolume},
		{"GET /creators/issues/{comicId}", s.HandleCreatorIssueCredits},
		{"GET /creators/{personId}", s.HandleCreatorDetail},
		{"GET /creators", s.HandleCreatorsList},
	}
}

func (s *Server) NewRouter() http.Handler {
	mux := http.NewServeMux()
	for _, rt := range s.routes() {
		mux.HandleFunc(rt.pattern, rt.handler)
	}
	if s.Admin != nil {
		s.Admin.Register(mux)
		s.Admin.RegisterAPI(mux)
	}
	var handler http.Handler = mux
	if !s.AuthOff {
		handler = RequireAuth(handler, s.Password, s.APIKey)
	}
	handler = corsMiddleware(handler)
	if s.Admin != nil && s.Admin.RequestLog != nil {
		handler = s.Admin.RequestLog.Middleware(handler)
	}
	return handler
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, PUT, POST, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Device-ID, Authorization")
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func pathToID(path string) string { return store.PathToID(path) }

func idToPath(id string) (string, error) {
	data, err := base64.RawURLEncoding.DecodeString(id)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
