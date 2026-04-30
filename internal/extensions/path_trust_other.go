//go:build !unix

package extensions

import "os"

func currentProcessUID() (int, bool) {
	return 0, false
}

func fileOwnerUID(_ os.FileInfo) (int, bool) {
	return 0, false
}
