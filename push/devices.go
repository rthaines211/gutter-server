package push

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// ReadDevices reads the token mirror the server keeps for the host watcher.
// A missing file means no devices yet.
func ReadDevices(path string) ([]Device, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ds []Device
	return ds, json.Unmarshal(b, &ds)
}

// WriteDevices replaces the mirror atomically so the watcher never reads a
// half-written file.
func WriteDevices(path string, ds []Device) error {
	if ds == nil {
		ds = []Device{}
	}
	b, err := json.Marshal(ds)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".push_devices-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
