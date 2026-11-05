package push

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testClient(t *testing.T) (*Client, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "AuthKey.p8")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := New(Config{KeyPath: path, KeyID: "KID123", TeamID: "TEAM456", BundleID: "com.example.app"})
	if err != nil {
		t.Fatal(err)
	}
	return c, key
}

func TestJWTSignsAndCaches(t *testing.T) {
	c, key := testClient(t)
	now := time.Unix(1_800_000_000, 0)
	c.Now = func() time.Time { return now }

	tok, err := c.token()
	if err != nil {
		t.Fatal(err)
	}
	again, _ := c.token()
	if again != tok {
		t.Fatal("token not cached within 50m")
	}

	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("not a JWT: %q", tok)
	}
	var hdr struct{ Alg, Kid string }
	var claims struct {
		Iss string
		Iat int64
	}
	decode := func(s string, v any) {
		b, err := base64.RawURLEncoding.DecodeString(s)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(b, v); err != nil {
			t.Fatal(err)
		}
	}
	decode(parts[0], &hdr)
	decode(parts[1], &claims)
	if hdr.Alg != "ES256" || hdr.Kid != "KID123" {
		t.Fatalf("header = %+v", hdr)
	}
	if claims.Iss != "TEAM456" || claims.Iat != now.Unix() {
		t.Fatalf("claims = %+v", claims)
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(sig) != 64 {
		t.Fatalf("sig len %d err %v", len(sig), err)
	}
	h := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	r, s := new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])
	if !ecdsa.Verify(&key.PublicKey, h[:], r, s) {
		t.Fatal("signature does not verify")
	}

	now = now.Add(51 * time.Minute)
	later, _ := c.token()
	if later == tok {
		t.Fatal("token not refreshed after 51m")
	}
}

type captured struct {
	path, topic, pushType, auth string
	body                        map[string]any
}

func apnsServer(t *testing.T, status int, reason string, got *captured) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got != nil {
			got.path = r.URL.Path
			got.topic = r.Header.Get("apns-topic")
			got.pushType = r.Header.Get("apns-push-type")
			got.auth = r.Header.Get("authorization")
			b, _ := io.ReadAll(r.Body)
			json.Unmarshal(b, &got.body)
		}
		w.WriteHeader(status)
		if reason != "" {
			json.NewEncoder(w).Encode(map[string]string{"reason": reason})
		}
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv
}

func TestSendRequestShape(t *testing.T) {
	c, _ := testClient(t)
	var got captured
	srv := apnsServer(t, 200, "", &got)
	c.Hosts["sandbox"] = srv.URL
	c.HTTP = srv.Client()

	err := c.Send(context.Background(), Device{Token: "abc", Env: "sandbox"},
		Notification{Title: "T", Body: "B", Screen: "review"})
	if err != nil {
		t.Fatal(err)
	}
	if got.path != "/3/device/abc" || got.topic != "com.example.app" || got.pushType != "alert" {
		t.Fatalf("got %+v", got)
	}
	if !strings.HasPrefix(got.auth, "bearer ") {
		t.Fatalf("auth = %q", got.auth)
	}
	aps, _ := got.body["aps"].(map[string]any)
	alert, _ := aps["alert"].(map[string]any)
	if alert["title"] != "T" || alert["body"] != "B" || aps["sound"] != "default" || aps["thread-id"] != "gutter" {
		t.Fatalf("aps = %+v", aps)
	}
	if got.body["screen"] != "review" {
		t.Fatalf("screen = %v", got.body["screen"])
	}
}

func TestSendOmitsEmptyScreen(t *testing.T) {
	c, _ := testClient(t)
	var got captured
	srv := apnsServer(t, 200, "", &got)
	c.Hosts["sandbox"] = srv.URL
	c.HTTP = srv.Client()
	if err := c.Send(context.Background(), Device{Token: "abc", Env: "sandbox"}, Notification{Title: "T"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := got.body["screen"]; ok {
		t.Fatalf("screen key present: %+v", got.body)
	}
}

func TestSend410IsUnregistered(t *testing.T) {
	c, _ := testClient(t)
	srv := apnsServer(t, 410, "Unregistered", nil)
	c.Hosts["production"] = srv.URL
	c.HTTP = srv.Client()
	err := c.Send(context.Background(), Device{Token: "abc", Env: "production"}, Notification{Title: "T"})
	if !errors.Is(err, ErrUnregistered) {
		t.Fatalf("err = %v", err)
	}
}

func TestSend400BadDeviceTokenIsPlainError(t *testing.T) {
	c, _ := testClient(t)
	srv := apnsServer(t, 400, "BadDeviceToken", nil)
	c.Hosts["production"] = srv.URL
	c.HTTP = srv.Client()
	err := c.Send(context.Background(), Device{Token: "abc", Env: "production"}, Notification{Title: "T"})
	if err == nil || errors.Is(err, ErrUnregistered) || !strings.Contains(err.Error(), "BadDeviceToken") {
		t.Fatalf("err = %v", err)
	}
}

func TestSendUnknownEnv(t *testing.T) {
	c, _ := testClient(t)
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer srv.Close()
	c.Hosts["staging"] = "" // even a stray empty entry must not be used
	c.HTTP = srv.Client()
	if err := c.Send(context.Background(), Device{Token: "abc", Env: "staging"}, Notification{Title: "T"}); err == nil {
		t.Fatal("want error for unknown env")
	}
	if called {
		t.Fatal("request was made")
	}
}
