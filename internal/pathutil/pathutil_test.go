package pathutil

import "testing"

func TestIsSafeStackPath(t *testing.T) {
	tests := []struct {
		name string
		path string
		want bool
	}{
		{"empty", "", true},
		{"simple", "envs/dev", true},
		{"nested", "a/b/c/d", true},
		{"single segment", "dev", true},
		{"dot segment", "./envs/dev", true},
		{"absolute unix", "/etc/passwd", false},
		{"parent traversal", "../secret", false},
		{"deep parent traversal", "../../etc/passwd", false},
		{"parent in middle", "envs/../../etc", false},
		{"dotdot only", "..", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsSafeStackPath(tt.path); got != tt.want {
				t.Errorf("IsSafeStackPath(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}
