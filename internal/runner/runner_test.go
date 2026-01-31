package runner

import "testing"

func TestParsePlanSummary(t *testing.T) {
	tests := []struct {
		name      string
		output    string
		added     int
		changed   int
		destroyed int
	}{
		{
			name:      "plan summary",
			output:    "Plan: 1 to add, 2 to change, 3 to destroy",
			added:     1,
			changed:   2,
			destroyed: 3,
		},
		{
			name:      "no changes",
			output:    "No changes. Your infrastructure matches the configuration.",
			added:     0,
			changed:   0,
			destroyed: 0,
		},
		{
			name:      "no differences",
			output:    "There are no differences between your configuration and the real world infrastructure.",
			added:     0,
			changed:   0,
			destroyed: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			added, changed, destroyed := parsePlanSummary(tt.output)
			if added != tt.added || changed != tt.changed || destroyed != tt.destroyed {
				t.Fatalf("got %d/%d/%d, want %d/%d/%d", added, changed, destroyed, tt.added, tt.changed, tt.destroyed)
			}
		})
	}
}
