package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/rthaines211/gutter-server/kosync"
	"github.com/rthaines211/gutter-server/store"
)

// kosyncReq sends a request the way KOReader does: x-auth-user/x-auth-key
// (MD5 of the password) and the kosync Accept header.
func (f *opdsFixture) kosyncReq(t *testing.T, method, path, body, key string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Accept", "application/vnd.koreader.v1+json")
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		req.Header.Set("x-auth-user", "kobo")
		req.Header.Set("x-auth-key", key)
	}
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	return rec
}

func (f *opdsFixture) cbzDigest(t *testing.T) string {
	t.Helper()
	fh, err := os.Open(f.cbzPath)
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	fi, _ := fh.Stat()
	d, err := kosync.PartialMD5(fh, fi.Size())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestKosyncAuthEndpoints(t *testing.T) {
	fx := newOPDSFixture(t)
	good := kosync.KeyFor(opdsTestPassword)
	tests := []struct {
		name     string
		method   string
		path     string
		body     string
		key      string
		basic    bool
		want     int
		wantBody string
	}{
		{"healthcheck open", "GET", "/kosync/healthcheck", "", "", false, 200, `"state":"OK"`},
		{"auth ok", "GET", "/kosync/users/auth", "", good, false, 200, `"authorized":"OK"`},
		{"auth key uppercase", "GET", "/kosync/users/auth", "", strings.ToUpper(good), false, 200, ""},
		{"auth plain password key", "GET", "/kosync/users/auth", "", opdsTestPassword, false, 200, ""},
		{"auth wrong key", "GET", "/kosync/users/auth", "", kosync.KeyFor("nope"), false, 401, `"code":2001`},
		{"auth no headers", "GET", "/kosync/users/auth", "", "", false, 401, `"code":2001`},
		{"auth basic fallback", "GET", "/kosync/users/auth", "", "", true, 200, ""},
		{"create ok", "POST", "/kosync/users/create", `{"username":"kobo","password":"` + good + `"}`, "", false, 201, `"username":"kobo"`},
		{"create wrong password", "POST", "/kosync/users/create", `{"username":"kobo","password":"` + kosync.KeyFor("x") + `"}`, "", false, 402, `"code":2002`},
		{"create bad body", "POST", "/kosync/users/create", `{"username":""}`, "", false, 403, `"code":2003`},
		{"progress needs auth", "GET", "/kosync/syncs/progress/abc", "", "", false, 401, `"code":2001`},
		{"put needs auth", "PUT", "/kosync/syncs/progress", `{}`, "", false, 401, ""},
	}
	t.Run("cross-site basic write refused", func(t *testing.T) {
		req := httptest.NewRequest("PUT", "/kosync/syncs/progress", strings.NewReader(`{}`))
		req.SetBasicAuth("kobo", opdsTestPassword)
		req.Header.Set("Sec-Fetch-Site", "cross-site")
		rec := httptest.NewRecorder()
		fx.handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("status %d, want 403", rec.Code)
		}
	})
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			if tc.key != "" {
				req.Header.Set("x-auth-user", "kobo")
				req.Header.Set("x-auth-key", tc.key)
			}
			if tc.basic {
				req.SetBasicAuth("kobo", opdsTestPassword)
			}
			rec := httptest.NewRecorder()
			fx.handler.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status %d, want %d: %s", rec.Code, tc.want, rec.Body.String())
			}
			if tc.wantBody != "" && !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Errorf("body %s missing %s", rec.Body.String(), tc.wantBody)
			}
			if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
				t.Errorf("content-type %q", ct)
			}
		})
	}
}

func TestKosyncAPIKey(t *testing.T) {
	fx := newOPDSFixture(t)
	fx.srv.APIKey = "apikey-1"
	fx.handler = fx.srv.NewRouter()
	for _, key := range []string{kosync.KeyFor("apikey-1"), "apikey-1"} {
		if rec := fx.kosyncReq(t, "GET", "/kosync/users/auth", "", key); rec.Code != 200 {
			t.Errorf("key %q: status %d", key, rec.Code)
		}
	}
}

func decodeProgress(t *testing.T, rec *httptest.ResponseRecorder) kosync.ProgressResponse {
	t.Helper()
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var p kosync.ProgressResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestKosyncProgressSync(t *testing.T) {
	fx := newOPDSFixture(t)
	key := kosync.KeyFor(opdsTestPassword)
	digest := fx.cbzDigest(t)

	// Unknown document: empty object.
	if rec := fx.kosyncReq(t, "GET", "/kosync/syncs/progress/"+digest, "", key); rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != "{}" {
		t.Fatalf("empty progress: %d %s", rec.Code, rec.Body.String())
	}

	// KOReader pushes page 2 of 3. The digest isn't indexed yet, so this
	// also exercises the on-demand library scan.
	put := `{"document":"` + digest + `","progress":"2","percentage":0.6667,"device":"Kobo Libra","device_id":"DEV1"}`
	rec := fx.kosyncReq(t, "PUT", "/kosync/syncs/progress", put, key)
	if rec.Code != 200 {
		t.Fatalf("put: %d %s", rec.Code, rec.Body.String())
	}
	var upd kosync.UpdateResponse
	json.Unmarshal(rec.Body.Bytes(), &upd)
	if upd.Document != digest || upd.Timestamp == 0 {
		t.Errorf("update response %+v", upd)
	}

	// Mirrored into Gutter's reading progress as 0-based page 1 of 3.
	gp, err := fx.srv.Store.GetProgress(fx.cbzID)
	if err != nil {
		t.Fatal(err)
	}
	if gp.CurrentPage != 1 || gp.TotalPages != 3 {
		t.Errorf("gutter progress = %d/%d, want 1/3", gp.CurrentPage, gp.TotalPages)
	}

	// GET returns KOReader's own record.
	p := decodeProgress(t, fx.kosyncReq(t, "GET", "/kosync/syncs/progress/"+digest, "", key))
	if p.Progress != "2" || p.Device != "Kobo Libra" || p.DeviceID != "DEV1" || p.Percentage != 0.6667 {
		t.Errorf("get after put = %+v", p)
	}

	// The Gutter app later reads to the last page (finished). Backdate
	// KOReader's record so the app's write is unambiguously newer.
	raw, _ := fx.srv.Store.GetKosyncProgress(digest)
	raw.Timestamp = raw.Timestamp.Add(-time.Hour)
	fx.srv.Store.UpsertKosyncProgress(*raw)
	if err := fx.srv.Store.UpsertProgress(store.Progress{ComicID: fx.cbzID, CurrentPage: 3, TotalPages: 3}); err != nil {
		t.Fatal(err)
	}
	p = decodeProgress(t, fx.kosyncReq(t, "GET", "/kosync/syncs/progress/"+digest, "", key))
	if p.Progress != "3" || p.Percentage != 1 || p.DeviceID != kosyncGutterDeviceID || p.Document != digest {
		t.Errorf("get after app read = %+v", p)
	}

	// KOReader pushes the last page: Gutter marks it finished.
	put = `{"document":"` + digest + `","progress":"3","percentage":1,"device":"Kobo Libra","device_id":"DEV1"}`
	if rec := fx.kosyncReq(t, "PUT", "/kosync/syncs/progress", put, key); rec.Code != 200 {
		t.Fatalf("put last: %d", rec.Code)
	}
	gp, _ = fx.srv.Store.GetProgress(fx.cbzID)
	if gp.CurrentPage != 3 {
		t.Errorf("last page should finish: current_page = %d", gp.CurrentPage)
	}
}

func TestKosyncFilenameDigestAndUnknownDocs(t *testing.T) {
	fx := newOPDSFixture(t)
	key := kosync.KeyFor(opdsTestPassword)

	// Filename matching method: MD5 of "Saga 001.cbz".
	fnDigest := kosync.FilenameDigest("Saga 001.cbz")
	put := `{"document":"` + fnDigest + `","progress":"1","percentage":0.33,"device":"KOReader","device_id":"D"}`
	if rec := fx.kosyncReq(t, "PUT", "/kosync/syncs/progress", put, key); rec.Code != 200 {
		t.Fatalf("put: %d %s", rec.Code, rec.Body.String())
	}
	if gp, err := fx.srv.Store.GetProgress(fx.cbzID); err != nil || gp.CurrentPage != 0 {
		t.Errorf("filename digest not mapped: %+v %v", gp, err)
	}

	// A book that isn't in the library is still stored and returned, so
	// KOReader devices can sync it among themselves.
	put = `{"document":"0123456789abcdef0123456789abcdef","progress":"/body/DocFragment[2]","percentage":0.1,"device":"KOReader","device_id":"D"}`
	if rec := fx.kosyncReq(t, "PUT", "/kosync/syncs/progress", put, key); rec.Code != 200 {
		t.Fatalf("put unknown: %d", rec.Code)
	}
	p := decodeProgress(t, fx.kosyncReq(t, "GET", "/kosync/syncs/progress/0123456789abcdef0123456789abcdef", "", key))
	if p.Progress != "/body/DocFragment[2]" || p.Percentage != 0.1 {
		t.Errorf("unknown doc round trip = %+v", p)
	}
}

func TestKosyncPutValidation(t *testing.T) {
	fx := newOPDSFixture(t)
	key := kosync.KeyFor(opdsTestPassword)
	tests := []struct {
		name string
		body string
	}{
		{"not json", "nope"},
		{"missing document", `{"progress":"1","percentage":0.1,"device":"K"}`},
		{"missing progress", `{"document":"abc","percentage":0.1,"device":"K"}`},
		{"missing device", `{"document":"abc","progress":"1","percentage":0.1}`},
		{"bad percentage", `{"document":"abc","progress":"1","percentage":2,"device":"K"}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := fx.kosyncReq(t, "PUT", "/kosync/syncs/progress", tc.body, key)
			if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), `"code":2003`) {
				t.Errorf("status %d body %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestKosyncAuthOff(t *testing.T) {
	fx := newOPDSFixture(t)
	fx.srv.AuthOff = true
	fx.handler = fx.srv.NewRouter()
	if rec := fx.kosyncReq(t, "GET", "/kosync/users/auth", "", ""); rec.Code != 200 {
		t.Errorf("auth off: users/auth %d", rec.Code)
	}
	body := `{"username":"x","password":"whatever"}`
	if rec := fx.kosyncReq(t, "POST", "/kosync/users/create", body, ""); rec.Code != 201 {
		t.Errorf("auth off: create %d", rec.Code)
	}
}

func TestKosyncReindexKeepsIndexWhenLibraryOffline(t *testing.T) {
	fx := newOPDSFixture(t)
	ix := fx.srv.kosyncIndexer()
	ix.reindex(fx.srv, true)
	digest := fx.cbzDigest(t)
	fx.srv.ComicDirs = []string{t.TempDir()} // volume "unmounted"
	ix.reindex(fx.srv, true)
	if ids, _ := fx.srv.Store.ComicIDsForKosyncDigest(digest); len(ids) != 1 {
		t.Errorf("offline library wiped the index: %v", ids)
	}
}

func TestKosyncReindexDropsMissingFiles(t *testing.T) {
	fx := newOPDSFixture(t)
	ix := fx.srv.kosyncIndexer()
	if !ix.reindex(fx.srv, true) {
		t.Fatal("forced reindex did not run")
	}
	digest := fx.cbzDigest(t)
	if id := fx.srv.kosyncLookup(digest); id != fx.cbzID {
		t.Fatalf("lookup = %q", id)
	}
	// Rate limit: an unforced run right after is skipped.
	if ix.reindex(fx.srv, false) {
		t.Error("unforced reindex should be rate-limited")
	}
	// Drop the CBZ but keep another file resolvable, so the volume isn't
	// "offline" and the stale row is pruned.
	if err := fx.srv.Store.UpsertKosyncDocument(store.KosyncDocument{ComicID: fx.cbrID, BinaryDigest: "x", FilenameDigest: "y"}); err != nil {
		t.Fatal(err)
	}
	os.Remove(fx.cbzPath)
	ix.reindex(fx.srv, true)
	if ids, _ := fx.srv.Store.ComicIDsForKosyncDigest(digest); len(ids) != 0 {
		t.Errorf("missing file still indexed: %v", ids)
	}
}
