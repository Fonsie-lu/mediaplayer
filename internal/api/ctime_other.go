//go:build !linux

package api

import "os"

func ctimeOf(info os.FileInfo) int64 {
	return info.ModTime().Unix()
}
