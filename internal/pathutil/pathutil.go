package pathutil

import (
	"os"
	"path/filepath"
	"strings"
)

// IsSafeStackPath returns true if the stack path is safe to use.
// It rejects absolute paths and paths that traverse above the root.
func IsSafeStackPath(stackPath string) bool {
	if stackPath == "" {
		return true
	}
	if filepath.IsAbs(stackPath) {
		return false
	}
	clean := filepath.Clean(stackPath)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
		return false
	}
	return true
}
