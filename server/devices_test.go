package server

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rthaines211/gutter-server/notify"
	"github.com/rthaines211/gutter-server/push"
)

func devicesServer(t *testing.T, secret string) *Server {
	t.Helper()
	st := newIntegrationTestStore(t)
	return &Server{Store: st, PushSecret: secret, AuthOff: true,
		Notifier: &notify.Notifier{Store: st, DevicesPath: t.TempDir() + "/push_devices.json"}}
}

func doDevices(t *testing.T, srv *Server, method, path, auth, body string) int {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	rec := httptest.NewRecorder()
	srv.NewRouter().ServeHTTP(rec, req)
	return rec.Code
}

func TestDevicesDisabledIs404(t *testing.T) {
	srv := devicesServer(t, "")
	if c := doDevices(t, srv, "POST", "/devices", "Bearer ", `{"token":"a","env":"sandbox"}`); c != 404 {
		t.Fatalf("POST = %d", c)
	}
	if c := doDevices(t, srv, "DELETE", "/devices/a", "Bearer ", ""); c != 404 {
		t.Fatalf("DELETE = %d", c)
	}
}

func TestDevicesRequiresSecret(t *testing.T) {
	srv := devicesServer(t, "s3cret")
	for _, auth := range []string{"", "Bearer wrong", "s3cret", "Basic s3cret"} {
		if c := doDevices(t, srv, "POST", "/devices", auth, `{"token":"a","env":"sandbox"}`); c != 401 {
			t.Fatalf("auth %q: %d", auth, c)
		}
	}
	if ds, _ := srv.Store.ListDeviceTokens(); len(ds) != 0 {
		t.Fatalf("stored %+v", ds)
	}
}

func TestDevicesRegisterAndDelete(t *testing.T) {
	srv := devicesServer(t, "s3cret")
	if c := doDevices(t, srv, "POST", "/devices", "Bearer s3cret", `{"token":"abc","env":"sandbox"}`); c != 204 {
		t.Fatalf("POST = %d", c)
	}
	mirror, _ := push.ReadDevices(srv.Notifier.DevicesPath)
	if len(mirror) != 1 || mirror[0] != (push.Device{Token: "abc", Env: "sandbox"}) {
		t.Fatalf("mirror = %+v", mirror)
	}
	if c := doDevices(t, srv, "DELETE", "/devices/abc", "Bearer s3cret", ""); c != 204 {
		t.Fatalf("DELETE = %d", c)
	}
	if ds, _ := srv.Store.ListDeviceTokens(); len(ds) != 0 {
		t.Fatalf("stored %+v", ds)
	}
	if mirror, _ := push.ReadDevices(srv.Notifier.DevicesPath); len(mirror) != 0 {
		t.Fatalf("mirror = %+v", mirror)
	}
}

func TestDevicesRejectsBadBody(t *testing.T) {
	srv := devicesServer(t, "s3cret")
	for _, body := range []string{`{"token":"","env":"sandbox"}`, `{"token":"a","env":"staging"}`, `not json`} {
		if c := doDevices(t, srv, "POST", "/devices", "Bearer s3cret", body); c != 400 {
			t.Fatalf("body %s: %d", body, c)
		}
	}
}
