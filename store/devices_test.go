package store

import (
	"reflect"
	"testing"

	"github.com/rthaines211/gutter-server/push"
)

func TestDeviceTokenUpsertUpdatesEnv(t *testing.T) {
	s := newTestStore(t)
	if err := s.UpsertDeviceToken("t1", "sandbox"); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertDeviceToken("t1", "production"); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListDeviceTokens()
	if err != nil {
		t.Fatal(err)
	}
	if want := []push.Device{{Token: "t1", Env: "production"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v", got)
	}
}

func TestDeviceTokenDelete(t *testing.T) {
	s := newTestStore(t)
	s.UpsertDeviceToken("t1", "sandbox")
	s.UpsertDeviceToken("t2", "sandbox")
	if err := s.DeleteDeviceToken("t1"); err != nil {
		t.Fatal(err)
	}
	got, _ := s.ListDeviceTokens()
	if want := []push.Device{{Token: "t2", Env: "sandbox"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v", got)
	}
}
