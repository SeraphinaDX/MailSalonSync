package jmap

import "strings"

// IsUploadQuotaExceeded reports whether err is the server rejecting a JMAP blob
// upload because its temporary upload quota has been exhausted. This is kept
// deliberately narrow so unrelated HTTP 403 responses still surface as real
// failures.
func IsUploadQuotaExceeded(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "jmap upload http 403") &&
		strings.Contains(s, "quota exceeded")
}
