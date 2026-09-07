package syncer

import "testing"

func TestLocalMaildirSeen(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"/mail/INBOX/new/12345", false},
		{"/mail/INBOX/cur/12345:2,", false},
		{"/mail/INBOX/cur/12345:2,S", true},
		{"/mail/INBOX/cur/12345:2,RS", true},
		{"/mail/INBOX/cur/12345:2,R", false},
	}
	for _, tt := range tests {
		if got := localMaildirSeen(tt.path); got != tt.want {
			t.Fatalf("localMaildirSeen(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
}
