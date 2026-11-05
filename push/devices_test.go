package push

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestDevicesRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "push_devices.json")
	want := []Device{{Token: "a", Env: "sandbox"}, {Token: "b", Env: "production"}}
	if err := WriteDevices(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := ReadDevices(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v", got)
	}
}

func TestReadDevicesMissingFile(t *testing.T) {
	got, err := ReadDevices(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil || got != nil {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestWriteDevicesLeavesNoTemp(t *testing.T) {
	dir := t.TempDir()
	if err := WriteDevices(filepath.Join(dir, "push_devices.json"), nil); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 || entries[0].Name() != "push_devices.json" {
		t.Fatalf("dir = %v", entries)
	}
}
