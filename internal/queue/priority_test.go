package queue

import "testing"

func TestTriggerPriority(t *testing.T) {
	tests := []struct {
		trigger string
		want    int
	}{
		{"scheduled", 1},
		{"cron", 1},
		{"manual", 2},
		{"webhook", 2},
		{"unknown", 2},
		{"", 2},
	}

	for _, tt := range tests {
		t.Run(tt.trigger, func(t *testing.T) {
			if got := TriggerPriority(tt.trigger); got != tt.want {
				t.Errorf("TriggerPriority(%q) = %d, want %d", tt.trigger, got, tt.want)
			}
		})
	}
}
