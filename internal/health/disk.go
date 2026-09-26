package health

import (
	"fmt"
	"syscall"
)

// FreeMB returns the space available to unprivileged processes on the
// filesystem that holds path, in MiB.
func FreeMB(path string) (int64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, fmt.Errorf("health: free space of %s: %w", path, err)
	}
	return int64(st.Bavail) * int64(st.Bsize) / (1 << 20), nil
}
