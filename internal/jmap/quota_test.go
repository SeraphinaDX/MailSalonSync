package jmap

import (
	"errors"
	"testing"
)

func TestIsUploadQuotaExceeded(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"stalwart upload quota", errors.New(`JMAP upload HTTP 403 Forbidden: {"title":"Quota exceeded"}`), true},
		{"different 403", errors.New("JMAP upload HTTP 403 Forbidden: permission denied"), false},
		{"different quota error", errors.New("JMAP API HTTP 403 Forbidden: Quota exceeded"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsUploadQuotaExceeded(tt.err); got != tt.want {
				t.Fatalf("IsUploadQuotaExceeded(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
