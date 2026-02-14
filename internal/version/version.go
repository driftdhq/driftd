package version

import (
	"os"
	"path/filepath"
	"strings"
)

type Versions struct {
	DefaultTerraform  string
	DefaultTerragrunt string
	StackTerraform    map[string]string
	StackTerragrunt   map[string]string
}

func Detect(projectDir string, stacks []string) (*Versions, error) {
	tfRoot := readVersionFile(filepath.Join(projectDir, ".terraform-version"))
	tgRoot := readVersionFile(filepath.Join(projectDir, ".terragrunt-version"))

	stackTF := make(map[string]string)
	stackTG := make(map[string]string)

	tfSet := map[string]struct{}{}
	tgSet := map[string]struct{}{}

	for _, stack := range stacks {
		stackDir := filepath.Join(projectDir, stack)
		tf := readVersionFile(filepath.Join(stackDir, ".terraform-version"))
		if tf == "" {
			tf = tfRoot
		}
		if tf != "" {
			stackTF[stack] = tf
			tfSet[tf] = struct{}{}
		}

		tg := readVersionFile(filepath.Join(stackDir, ".terragrunt-version"))
		if tg == "" {
			tg = tgRoot
		}
		if tg != "" {
			stackTG[stack] = tg
			tgSet[tg] = struct{}{}
		}
	}

	tfDefault, tfStack := collapseIfSingle(tfSet, stackTF)
	tgDefault, tgStack := collapseIfSingle(tgSet, stackTG)

	// Prefer explicit root versions if they exist.
	if tfRoot != "" {
		tfDefault = tfRoot
		tfStack = dropDefault(tfStack, tfRoot)
	}
	if tgRoot != "" {
		tgDefault = tgRoot
		tgStack = dropDefault(tgStack, tgRoot)
	}

	return &Versions{
		DefaultTerraform:  tfDefault,
		DefaultTerragrunt: tgDefault,
		StackTerraform:    tfStack,
		StackTerragrunt:   tgStack,
	}, nil
}

func readVersionFile(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func collapseIfSingle(set map[string]struct{}, stack map[string]string) (string, map[string]string) {
	if len(set) == 1 {
		for v := range set {
			return v, map[string]string{}
		}
	}
	return "", stack
}

func dropDefault(stack map[string]string, def string) map[string]string {
	if def == "" || len(stack) == 0 {
		return stack
	}
	out := make(map[string]string, len(stack))
	for k, v := range stack {
		if v != def {
			out[k] = v
		}
	}
	return out
}
