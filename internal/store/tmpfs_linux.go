//go:build linux

package store

import (
	"fmt"
	"syscall"
)

// Filesystem magic numbers accepted as "tmpfs-like": tmpfs itself and ramfs. Both live in
// kernel-managed RAM and disappear on unmount, which is what hard constraint 4 needs.
const (
	tmpfsMagic = 0x01021994
	ramfsMagic = 0x858458F6
)

// isTmpfsBacked reports whether path resides on a tmpfs-like filesystem.
func isTmpfsBacked(path string) (bool, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return false, fmt.Errorf("statfs %q: %w", path, err)
	}
	switch uint32(st.Type) {
	case tmpfsMagic, ramfsMagic:
		return true, nil
	}
	return false, nil
}
