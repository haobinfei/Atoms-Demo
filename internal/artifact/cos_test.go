package artifact

import "testing"

func TestMatchesObjectPrefix(t *testing.T) {
	tests := []struct {
		name   string
		key    string
		prefix string
		want   bool
	}{
		{name: "exact", key: "sessions/abc", prefix: "sessions/abc", want: true},
		{name: "child", key: "sessions/abc/current.json", prefix: "sessions/abc", want: true},
		{name: "sibling", key: "sessions/abc-2/current.json", prefix: "sessions/abc", want: false},
		{name: "provider sibling", key: "sessions/abc/contest/gemma-2/current.json", prefix: "sessions/abc/contest/gemma", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := matchesObjectPrefix(tt.key, tt.prefix); got != tt.want {
				t.Fatalf("matchesObjectPrefix(%q, %q) = %v, want %v", tt.key, tt.prefix, got, tt.want)
			}
		})
	}
}
