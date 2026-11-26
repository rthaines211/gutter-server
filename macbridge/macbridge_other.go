//go:build !darwin || !cgo

package macbridge

import "errors"

// Open is only available on macOS with cgo.
func Open(string) (string, error) { return "", errors.New("macbridge: macOS only") }
