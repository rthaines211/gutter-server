package server

import (
	"archive/zip"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/rthaines211/gutter-server/cache"
	"github.com/rthaines211/gutter-server/store"
)

var bubblePages = [][]byte{[]byte("page-zero"), []byte("page-one"), []byte("page-two")}

func sha1Hex(b []byte) string { h := sha1.Sum(b); return hex.EncodeToString(h[:]) }

func writeSidecar(t *testing.T, dir, sha, body string) {
	t.Helper()
	p := filepath.Join(dir, sha[:2], sha+".json")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// setupBubblesTest: a 3-page CBZ with distinct page bytes. Page 0 has a
// 3-balloon sidecar, page 1 is unprocessed, page 2 was processed with none.
func setupBubblesTest(t *testing.T) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	f, err := os.Create(filepath.Join(dir, "b.cbz"))
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for i, p := range bubblePages {
		w, _ := zw.Create(fmt.Sprintf("p%d.jpg", i))
		w.Write(p)
	}
	zw.Close()
	f.Close()
	st, err := store.NewStore(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	bdir := t.TempDir()
	writeSidecar(t, bdir, sha1Hex(bubblePages[0]),
		`{"v":1,"w":100,"h":150,"bubbles":[{"r":[1,2,3,4],"label":1},{"r":[5,6,7,8],"label":2},{"r":[9,9,9,9],"label":3}],"mask":"UE5H"}`)
	writeSidecar(t, bdir, sha1Hex(bubblePages[2]), `{"v":1,"w":100,"h":150,"bubbles":[]}`)
	return &Server{ComicDirs: []string{dir}, Cache: cache.NewLRU(10), Store: st, BubblesDir: bdir}, pathToID("b.cbz")
}

func callBubbles(h http.HandlerFunc, method, id, num, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "/x", strings.NewReader(body))
	req.SetPathValue("id", id)
	if num != "" {
		req.SetPathValue("num", num)
	}
	w := httptest.NewRecorder()
	h(w, req)
	return w
}

func labelsIn(t *testing.T, r io.Reader) []int {
	t.Helper()
	var sc bubbleSidecar
	if err := json.NewDecoder(r).Decode(&sc); err != nil {
		t.Fatal(err)
	}
	out := []int{}
	for _, b := range sc.Bubbles {
		out = append(out, b.Label)
	}
	return out
}

func TestPageBubblesServesSidecar(t *testing.T) {
	srv, id := setupBubblesTest(t)
	w := callBubbles(srv.HandlePageBubbles, "GET", id, "0", "")
	if w.Code != 200 {
		t.Fatalf("got %d: %s", w.Code, w.Body.String())
	}
	if cc := w.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("Cache-Control = %q", cc)
	}
	body := w.Body.String()
	if !strings.Contains(body, `"mask":"UE5H"`) {
		t.Errorf("mask missing: %s", body)
	}
	if got := labelsIn(t, strings.NewReader(body)); !reflect.DeepEqual(got, []int{1, 2, 3}) {
		t.Errorf("labels %v", got)
	}
}

func TestPageBubblesNotFound(t *testing.T) {
	srv, id := setupBubblesTest(t)
	for _, num := range []string{"1", "99"} {
		if w := callBubbles(srv.HandlePageBubbles, "GET", id, num, ""); w.Code != 404 {
			t.Errorf("page %s: got %d", num, w.Code)
		}
	}
	if w := callBubbles(srv.HandlePageBubbles, "GET", id, "x", ""); w.Code != 400 {
		t.Errorf("bad num: got %d", w.Code)
	}
	srv.BubblesDir = ""
	if w := callBubbles(srv.HandlePageBubbles, "GET", id, "0", ""); w.Code != 404 {
		t.Errorf("feature off: got %d", w.Code)
	}
}

func TestPageBubblesZeroBalloons(t *testing.T) {
	srv, id := setupBubblesTest(t)
	w := callBubbles(srv.HandlePageBubbles, "GET", id, "2", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"bubbles":[]`) {
		t.Fatalf("got %d: %s", w.Code, w.Body.String())
	}
}

func TestPutBubbleOrderAppliesAndResets(t *testing.T) {
	srv, id := setupBubblesTest(t)
	if w := callBubbles(srv.HandlePutBubbleOrder, "PUT", id, "0", `{"labels":[2,1,3]}`); w.Code != 204 {
		t.Fatalf("put: %d %s", w.Code, w.Body.String())
	}
	if got := labelsIn(t, callBubbles(srv.HandlePageBubbles, "GET", id, "0", "").Body); !reflect.DeepEqual(got, []int{2, 1, 3}) {
		t.Errorf("after swap %v", got)
	}
	var bulk struct {
		Pages map[string]bubbleSidecar `json:"pages"`
	}
	json.NewDecoder(callBubbles(srv.HandleComicBubbles, "GET", id, "", "").Body).Decode(&bulk)
	if l := bulk.Pages["0"].Bubbles; len(l) != 3 || l[0].Label != 2 {
		t.Errorf("bulk not reordered: %+v", l)
	}
	if w := callBubbles(srv.HandlePutBubbleOrder, "PUT", id, "0", `{"labels":[1,2,3]}`); w.Code != 204 {
		t.Fatalf("reset: %d", w.Code)
	}
	if got, _ := srv.Store.GetBubbleOrder(sha1Hex(bubblePages[0])); got != nil {
		t.Errorf("stored order should be deleted, got %v", got)
	}
}

func TestBalloonTextFollowsSavedOrder(t *testing.T) {
	srv, id := setupBubblesTest(t)
	writeSidecar(t, srv.BubblesDir, sha1Hex(bubblePages[0]),
		`{"v":1,"w":100,"h":150,"bubbles":[{"r":[1,2,3,4],"label":1,"t":"HELLO THERE."},{"r":[5,6,7,8],"label":2,"t":"WHO, ME?"}]}`)
	if w := callBubbles(srv.HandlePutBubbleOrder, "PUT", id, "0", `{"labels":[2,1]}`); w.Code != 204 {
		t.Fatalf("put: %d %s", w.Code, w.Body.String())
	}
	var sc bubbleSidecar
	json.NewDecoder(callBubbles(srv.HandlePageBubbles, "GET", id, "0", "").Body).Decode(&sc)
	if len(sc.Bubbles) != 2 || sc.Bubbles[0].T != "WHO, ME?" || sc.Bubbles[1].T != "HELLO THERE." {
		t.Errorf("text lost or not reordered: %+v", sc.Bubbles)
	}
}

func TestPutBubbleOrderRejects(t *testing.T) {
	srv, id := setupBubblesTest(t)
	for _, body := range []string{`{"labels":[1,2]}`, `{"labels":[1,1,2]}`, `{"labels":[1,2,4]}`, `{bad`} {
		if w := callBubbles(srv.HandlePutBubbleOrder, "PUT", id, "0", body); w.Code != 400 {
			t.Errorf("%s: got %d", body, w.Code)
		}
	}
	if w := callBubbles(srv.HandlePutBubbleOrder, "PUT", id, "1", `{"labels":[]}`); w.Code != 404 {
		t.Errorf("unprocessed page: got %d", w.Code)
	}
}

func TestComicBubblesOmitsUnprocessed(t *testing.T) {
	srv, id := setupBubblesTest(t)
	w := callBubbles(srv.HandleComicBubbles, "GET", id, "", "")
	var bulk struct {
		Pages map[string]json.RawMessage `json:"pages"`
	}
	json.NewDecoder(w.Body).Decode(&bulk)
	if len(bulk.Pages) != 2 || bulk.Pages["0"] == nil || bulk.Pages["2"] == nil {
		t.Fatalf("pages = %v", bulk.Pages)
	}
	if w := callBubbles(srv.HandleComicBubbles, "GET", "nope", "", ""); w.Code != 404 {
		t.Errorf("unknown comic: %d", w.Code)
	}
}

func TestBubblesSurviveRename(t *testing.T) {
	srv, id := setupBubblesTest(t)
	callBubbles(srv.HandlePutBubbleOrder, "PUT", id, "0", `{"labels":[3,2,1]}`)
	src, _ := os.ReadFile(filepath.Join(srv.ComicDirs[0], "b.cbz"))
	os.WriteFile(filepath.Join(srv.ComicDirs[0], "renamed.cbz"), src, 0o644)
	w := callBubbles(srv.HandlePageBubbles, "GET", pathToID("renamed.cbz"), "0", "")
	if got := labelsIn(t, w.Body); !reflect.DeepEqual(got, []int{3, 2, 1}) {
		t.Errorf("renamed copy: %v", got)
	}
}

func TestStaleBubbleOrderIgnored(t *testing.T) {
	srv, id := setupBubblesTest(t)
	srv.Store.SetBubbleOrder(sha1Hex(bubblePages[0]), []int{7, 8})
	w := callBubbles(srv.HandlePageBubbles, "GET", id, "0", "")
	if got := labelsIn(t, w.Body); !reflect.DeepEqual(got, []int{1, 2, 3}) {
		t.Errorf("stale order applied: %v", got)
	}
}

// A truncated or garbage sidecar must not take down the whole book: the page
// reads as unprocessed (chunk pan) and the bulk download keeps the other pages.
func TestUnparseableSidecarIsSkipped(t *testing.T) {
	srv, id := setupBubblesTest(t)
	writeSidecar(t, srv.BubblesDir, sha1Hex(bubblePages[2]), `{"v":1,"w":10`)
	if w := callBubbles(srv.HandlePageBubbles, "GET", id, "2", ""); w.Code != 404 {
		t.Errorf("single page: got %d", w.Code)
	}
	w := callBubbles(srv.HandleComicBubbles, "GET", id, "", "")
	var bulk struct {
		Pages map[string]json.RawMessage `json:"pages"`
	}
	json.NewDecoder(w.Body).Decode(&bulk)
	if w.Code != 200 || len(bulk.Pages) != 1 || bulk.Pages["0"] == nil {
		t.Fatalf("bulk: %d %v", w.Code, bulk.Pages)
	}
}

// setupIndexedTest lays the library out like prod: comic dirs and .bubbles share a
// root (/media/Comics), so the host job's index is keyed by the path under it.
func setupIndexedTest(t *testing.T) (srv *Server, id, cbz string) {
	t.Helper()
	lib := t.TempDir()
	shelf := filepath.Join(lib, "Shelf")
	os.MkdirAll(shelf, 0o755)
	cbz = filepath.Join(shelf, "b.cbz")
	f, _ := os.Create(cbz)
	zw := zip.NewWriter(f)
	for i, p := range bubblePages {
		w, _ := zw.Create(fmt.Sprintf("p%d.jpg", i))
		w.Write(p)
	}
	zw.Close()
	f.Close()
	st, err := store.NewStore(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	srv = &Server{ComicDirs: []string{shelf}, Cache: cache.NewLRU(10), Store: st, BubblesDir: filepath.Join(lib, ".bubbles")}
	return srv, pathToID("b.cbz"), cbz
}

func writeBookIndex(t *testing.T, srv *Server, cbz string, sizeDelta int64, pages map[string]string) {
	t.Helper()
	fi, _ := os.Stat(cbz)
	rel, _ := filepath.Rel(filepath.Dir(srv.BubblesDir), cbz)
	sum := sha1.Sum([]byte(rel))
	body, _ := json.Marshal(map[string]any{"v": 1, "size": fi.Size() + sizeDelta, "mtime": fi.ModTime().Unix(), "pages": pages})
	p := filepath.Join(srv.BubblesDir, "books", hex.EncodeToString(sum[:])+".json")
	os.MkdirAll(filepath.Dir(p), 0o755)
	os.WriteFile(p, body, 0o644)
}

// The index is trusted while the file is unchanged: page 1's entry points at a
// sidecar its real bytes would never hash to, which proves no page was read.
func TestBookIndexServesWithoutReadingPages(t *testing.T) {
	srv, id, cbz := setupIndexedTest(t)
	fake := strings.Repeat("ab", 20)
	writeSidecar(t, srv.BubblesDir, fake, `{"v":1,"w":1,"h":1,"bubbles":[{"r":[1,1,1,1],"label":1}]}`)
	writeBookIndex(t, srv, cbz, 0, map[string]string{"p1.jpg": fake})
	if w := callBubbles(srv.HandlePageBubbles, "GET", id, "1", ""); w.Code != 200 {
		t.Fatalf("page via index: %d", w.Code)
	}
	var bulk struct {
		Pages map[string]json.RawMessage `json:"pages"`
	}
	json.NewDecoder(callBubbles(srv.HandleComicBubbles, "GET", id, "", "").Body).Decode(&bulk)
	if len(bulk.Pages) != 1 || bulk.Pages["1"] == nil {
		t.Fatalf("bulk via index: %v", bulk.Pages)
	}
}

func TestStaleBookIndexFallsBackToHashing(t *testing.T) {
	srv, id, cbz := setupIndexedTest(t)
	fake := strings.Repeat("ab", 20)
	writeSidecar(t, srv.BubblesDir, fake, `{"v":1,"w":1,"h":1,"bubbles":[]}`)
	writeSidecar(t, srv.BubblesDir, sha1Hex(bubblePages[1]), `{"v":1,"w":2,"h":2,"bubbles":[]}`)
	writeBookIndex(t, srv, cbz, 1, map[string]string{"p1.jpg": fake}) // size no longer matches
	w := callBubbles(srv.HandlePageBubbles, "GET", id, "1", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"w":2`) {
		t.Fatalf("stale index must be ignored: %d %s", w.Code, w.Body.String())
	}
}

func TestBookIndexCannotEscapeTheBubblesDir(t *testing.T) {
	srv, id, cbz := setupIndexedTest(t)
	writeBookIndex(t, srv, cbz, 0, map[string]string{"p1.jpg": "../../../../etc/passwd", "p2.jpg": "a"})
	for _, num := range []string{"1", "2"} {
		if w := callBubbles(srv.HandlePageBubbles, "GET", id, num, ""); w.Code != 404 {
			t.Errorf("page %s: got %d", num, w.Code)
		}
	}
}

func TestPanelsPassThrough(t *testing.T) {
	srv, id := setupBubblesTest(t)
	// page 0's sidecar has no panels: no key
	if body := callBubbles(srv.HandlePageBubbles, "GET", id, "0", "").Body.String(); strings.Contains(body, `"panels"`) {
		t.Errorf("unexpected panels key: %s", body)
	}
	writeSidecar(t, srv.BubblesDir, sha1Hex(bubblePages[0]),
		`{"v":1,"w":100,"h":150,"bubbles":[],"panels":[[0,0,48,150],[52,0,100,150]]}`)
	w := callBubbles(srv.HandlePageBubbles, "GET", id, "0", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"panels":[[0,0,48,150],[52,0,100,150]]`) {
		t.Fatalf("got %d: %s", w.Code, w.Body.String())
	}
	w = callBubbles(srv.HandleComicBubbles, "GET", id, "", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"panels":[[0,0,48,150],[52,0,100,150]]`) {
		t.Fatalf("whole book: got %d: %s", w.Code, w.Body.String())
	}
}
