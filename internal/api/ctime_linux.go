//go:build linux

package api

import (
	"os"
	"syscall"
)

func ctimeOf(info os.FileInfo) int64 {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return st.Ctim.Sec
	}
	return info.ModTime().Unix()
}
