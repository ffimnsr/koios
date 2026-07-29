package cli

import (
	"fmt"
	"math"
)

func checkedFD(fd uintptr) (int, error) {
	if fd > uintptr(math.MaxInt) {
		return 0, fmt.Errorf("file descriptor %d exceeds max int", fd)
	}
	return int(fd), nil
}
