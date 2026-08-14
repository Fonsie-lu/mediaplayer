//go:build linux

package api

import "syscall"

// statfsUsage returns total and unprivileged-available bytes for the
// filesystem mounted at path.
func statfsUsage(path string) (uint64, uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, err
	}
	bs := uint64(st.Bsize)
	return st.Blocks * bs, st.Bavail * bs, nil
}
