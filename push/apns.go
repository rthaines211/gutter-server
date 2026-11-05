// Package push sends Apple push notifications with token (.p8) auth. Stdlib
// only, so the host watcher (cmd/pushwatch) can build it without cgo.
package push

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"time"
)

// ErrUnregistered means APNs answered 410: the token is gone for good and
// should be forgotten. Every other failure keeps the token.
var ErrUnregistered = errors.New("apns: device unregistered")

// jwtTTL stays under APNs' one-hour limit on provider tokens.
const jwtTTL = 50 * time.Minute

type Device struct {
	Token string `json:"token"`
	Env   string `json:"env"` // sandbox | production
}

type Notification struct {
	Title, Body string
	Screen      string // "read", "review" or "" — where a tap lands in the app
}

type Config struct {
	KeyPath, KeyID, TeamID, BundleID string
}

type Client struct {
	HTTP  *http.Client
	Now   func() time.Time
	Hosts map[string]string // env → base URL

	cfg Config
	key *ecdsa.PrivateKey

	mu    sync.Mutex
	jwt   string
	jwtAt time.Time
}

func New(c Config) (*Client, error) {
	raw, err := os.ReadFile(c.KeyPath)
	if err != nil {
		return nil, fmt.Errorf("push: reading key: %w", err)
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, fmt.Errorf("push: %s is not PEM", c.KeyPath)
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("push: parsing key: %w", err)
	}
	key, ok := k.(*ecdsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("push: key is %T, want ECDSA", k)
	}
	return &Client{
		HTTP: &http.Client{Timeout: 15 * time.Second},
		Now:  time.Now,
		Hosts: map[string]string{
			"sandbox":    "https://api.sandbox.push.apple.com",
			"production": "https://api.push.apple.com",
		},
		cfg: c,
		key: key,
	}, nil
}

func (c *Client) token() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.Now()
	if c.jwt != "" && now.Sub(c.jwtAt) < jwtTTL {
		return c.jwt, nil
	}
	enc := func(v any) string {
		b, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(b)
	}
	unsigned := enc(map[string]string{"alg": "ES256", "kid": c.cfg.KeyID}) + "." +
		enc(map[string]any{"iss": c.cfg.TeamID, "iat": now.Unix()})
	h := sha256.Sum256([]byte(unsigned))
	r, s, err := ecdsa.Sign(rand.Reader, c.key, h[:])
	if err != nil {
		return "", err
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	c.jwt = unsigned + "." + base64.RawURLEncoding.EncodeToString(sig)
	c.jwtAt = now
	return c.jwt, nil
}

func (c *Client) Send(ctx context.Context, d Device, n Notification) error {
	host := c.Hosts[d.Env]
	if host == "" {
		return fmt.Errorf("push: unknown env %q", d.Env)
	}
	payload := map[string]any{
		"aps": map[string]any{
			"alert":     map[string]string{"title": n.Title, "body": n.Body},
			"sound":     "default",
			"thread-id": "comic-reader",
		},
	}
	if n.Screen != "" {
		payload["screen"] = n.Screen
	}
	body, _ := json.Marshal(payload)
	jwt, err := c.token()
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", host+"/3/device/"+d.Token, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("authorization", "bearer "+jwt)
	req.Header.Set("apns-topic", c.cfg.BundleID)
	req.Header.Set("apns-push-type", "alert")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		return nil
	}
	var r struct{ Reason string }
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	json.Unmarshal(b, &r)
	if resp.StatusCode == http.StatusGone {
		return fmt.Errorf("%w (%s)", ErrUnregistered, r.Reason)
	}
	return fmt.Errorf("apns: %d %s", resp.StatusCode, r.Reason)
}
