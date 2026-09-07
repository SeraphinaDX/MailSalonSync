package jmap

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestImportRaw(t *testing.T) {
	var sawUpload, sawImport, sawSeen bool
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/upload/acct":
			sawUpload = true
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatal(err)
			}
			if string(body) != "Subject: test\r\n\r\nbody\r\n" {
				t.Fatalf("unexpected upload body %q", string(body))
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"blobId":"blob1"}`)
		case "/api":
			var req struct {
				MethodCalls []json.RawMessage `json:"methodCalls"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatal(err)
			}
			if len(req.MethodCalls) != 1 {
				t.Fatalf("methodCalls = %d, want 1", len(req.MethodCalls))
			}
			var call []json.RawMessage
			if err := json.Unmarshal(req.MethodCalls[0], &call); err != nil {
				t.Fatal(err)
			}
			var method string
			if err := json.Unmarshal(call[0], &method); err != nil {
				t.Fatal(err)
			}
			if method != "Email/import" {
				t.Fatalf("method = %q, want Email/import", method)
			}
			var args struct {
				Emails map[string]struct {
					BlobID     string          `json:"blobId"`
					MailboxIDs map[string]bool `json:"mailboxIds"`
					Keywords   map[string]bool `json:"keywords"`
				} `json:"emails"`
			}
			if err := json.Unmarshal(call[1], &args); err != nil {
				t.Fatal(err)
			}
			msg := args.Emails["message"]
			if msg.BlobID != "blob1" || !msg.MailboxIDs["inbox"] {
				t.Fatalf("bad import args: %#v", msg)
			}
			sawSeen = msg.Keywords["$seen"]
			sawImport = true
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"methodResponses":[["Email/import",{"created":{"message":{"id":"email1"}}},"0"]]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := &Client{
		http: srv.Client(),
		session: Session{
			UploadURL: srv.URL + "/upload/{accountId}",
			APIURL:    srv.URL + "/api",
		},
		accountID: "acct",
	}
	id, err := c.ImportRaw(context.Background(), []byte("Subject: test\r\n\r\nbody\r\n"), "inbox", true)
	if err != nil {
		t.Fatal(err)
	}
	if id != "email1" {
		t.Fatalf("id = %q, want email1", id)
	}
	if !sawUpload || !sawImport || !sawSeen {
		t.Fatalf("sawUpload=%v sawImport=%v sawSeen=%v", sawUpload, sawImport, sawSeen)
	}
}
