//go:build !linux

package api

import "errors"

// statfsUsage is Linux-only (same build-tag split as ctime_linux.go /
// ctime_other.go); elsewhere the error keeps the disk widget hidden.
func statfsUsage(string) (uint64, uint64, error) {
	return 0, 0, errors.New("disk usage unsupported on this platform")
}
