//go:build unix

package extensions

import (
	"os"
	"syscall"
)

func currentProcessUID() (int, bool) {
	return os.Geteuid(), true
}

func fileOwnerUID(info os.FileInfo) (int, bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat == nil {
		return 0, false
	}
	return int(stat.Uid), true
}
