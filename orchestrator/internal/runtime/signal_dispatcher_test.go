package runtime

import "testing"

func TestSignalTargetFromSubject(t *testing.T) {
	tests := []struct {
		subject string
		want    string
	}{
		{subject: "signals.bot-123", want: "bot-123"},
		{subject: "bars.bot-456", want: "bot-456"},
		{subject: "", want: ""},
	}

	for _, tt := range tests {
		if got := signalTargetFromSubject(tt.subject); got != tt.want {
			t.Fatalf("signalTargetFromSubject(%q) = %q, want %q", tt.subject, got, tt.want)
		}
	}
}
