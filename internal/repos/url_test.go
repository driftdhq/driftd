package repos

import "testing"

func TestCanonicalURL(t *testing.T) {
	cases := []struct {
		name  string
		raw   string
		want  string
		valid bool
	}{
		{
			name:  "https",
			raw:   "https://github.com/org/repo.git",
			want:  "github.com/org/repo",
			valid: true,
		},
		{
			name:  "ssh_scp_style",
			raw:   "git@github.com:org/repo.git",
			want:  "github.com/org/repo",
			valid: true,
		},
		{
			name:  "ssh_url_style",
			raw:   "ssh://git@github.com/org/repo.git",
			want:  "github.com/org/repo",
			valid: true,
		},
		{
			name:  "html_url",
			raw:   "https://github.com/org/repo",
			want:  "github.com/org/repo",
			valid: true,
		},
		{
			name:  "local_path",
			raw:   "/tmp/repo",
			want:  "local:/tmp/repo",
			valid: true,
		},
		{
			name:  "invalid_empty",
			raw:   "   ",
			valid: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := CanonicalURL(tc.raw)
			if ok != tc.valid {
				t.Fatalf("valid=%v, want %v", ok, tc.valid)
			}
			if got != tc.want {
				t.Fatalf("canonical=%q, want %q", got, tc.want)
			}
		})
	}
}
