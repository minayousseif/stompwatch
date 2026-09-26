package tailnet

import (
	"errors"
	"io/fs"
	"os"
)

// readFileOrEmpty returns the file's contents, or "" when it was never
// written, which is how the fake records "the program never ran".
func readFileOrEmpty(path string) (string, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	return string(b), err
}
