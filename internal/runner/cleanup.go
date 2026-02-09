package runner

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

var cleanupDirNames = map[string]struct{}{
	".terraform":          {},
	".terragrunt-cache":   {},
	"terraform.tfstate.d": {},
}

// CleanupWorkspaceArtifacts removes terraform/terragrunt artifacts from a workspace snapshot.
func CleanupWorkspaceArtifacts(root string) error {
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if info, infoErr := entry.Info(); infoErr == nil && info.Mode()&os.ModeSymlink != 0 {
			return nil
		}
		name := entry.Name()
		if entry.IsDir() {
			if _, ok := cleanupDirNames[name]; ok {
				if err := os.RemoveAll(path); err != nil {
					return err
				}
				return fs.SkipDir
			}
			return nil
		}

		// Keep .terraform.lock.hcl for reproducibility; it influences provider selection.
		if name == "crash.log" || name == ".terraform.tfstate.lock.info" || name == "errored.tfstate" || isTerraformStateFile(name) {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
		return nil
	})
}

func isTerraformStateFile(name string) bool {
	if strings.HasSuffix(name, ".tfstate") {
		return true
	}
	return strings.Contains(name, ".tfstate.")
}
