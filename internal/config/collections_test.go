package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCollectionsOnlyConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	data := `[[collections]]
name = "contacts"
protocol = "carddav"
remote = "https://example.test/dav/book/"
local_dir = "~/Contacts"
[collections.dav]
username = "me"
password_env = "DAV_SECRET"
[[collections]]
name = "calendar"
protocol = "jmap-calendars"
remote = "calendar-id"
local_dir = "~/Calendar"
[collections.jmap]
session_url = "https://example.test/jmap/session"
auth = "bearer"
bearer_token_env = "JMAP_TOKEN"
`
	os.WriteFile(path, []byte(data), 0600)
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Collections) != 2 || len(c.Accounts) != 0 || strings.HasPrefix(c.Collections[0].LocalDir, "~") {
		t.Fatalf("bad normalization: %+v", c)
	}
	for _, replace := range [][2]string{{`protocol = "carddav"`, `protocol = "unknown"`}, {`https://example.test/dav/book/`, `http://example.test/book/`}, {`name = "calendar"`, `name = "contacts"`}, {`local_dir = "~/Calendar"`, `local_dir = "~/Contacts"`}, {`auth = "bearer"`, `auth = "wrong"`}, {`local_dir = "~/Contacts"`, `local_dr = "~/Contacts"`}} {
		os.WriteFile(path, []byte(strings.Replace(data, replace[0], replace[1], 1)), 0600)
		if _, err := Load(path); err == nil {
			t.Fatalf("accepted invalid config: %s", replace[1])
		}
	}
}
