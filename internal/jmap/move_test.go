package jmap

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMoveBetweenMailboxes(t *testing.T) {
	var sawMove bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			MethodCalls [][]json.RawMessage `json:"methodCalls"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		if len(req.MethodCalls) != 1 || len(req.MethodCalls[0]) < 2 {
			t.Fatalf("bad JMAP request: %#v", req.MethodCalls)
		}
		var method string
		if err := json.Unmarshal(req.MethodCalls[0][0], &method); err != nil {
			t.Fatal(err)
		}

		w.Header().Set("Content-Type", "application/json")
		switch method {
		case "Email/get":
			_, _ = w.Write([]byte(`{"methodResponses":[["Email/get",{"accountId":"acc","state":"s","list":[{"id":"email1","blobId":"blob1","mailboxIds":{"inbox":true},"keywords":{}}]},"0"]]}`))
		case "Email/set":
			var args struct {
				Update map[string]map[string]any `json:"update"`
			}
			if err := json.Unmarshal(req.MethodCalls[0][1], &args); err != nil {
				t.Fatal(err)
			}
			patch := args.Update["email1"]
			if v, ok := patch["mailboxIds/archive"].(bool); !ok || !v {
				t.Fatalf("destination membership patch = %#v", patch["mailboxIds/archive"])
			}
			if v, ok := patch["mailboxIds/inbox"]; !ok || v != nil {
				t.Fatalf("source membership patch = %#v", v)
			}
			sawMove = true
			_, _ = w.Write([]byte(`{"methodResponses":[["Email/set",{"accountId":"acc","oldState":"s","newState":"s2","updated":{"email1":null}},"0"]]}`))
		default:
			t.Fatalf("unexpected method %q", method)
		}
	}))
	defer srv.Close()

	c := &Client{
		http:      srv.Client(),
		session:   Session{APIURL: srv.URL},
		accountID: "acc",
	}
	if err := c.MoveBetweenMailboxes(context.Background(), "email1", "inbox", "archive"); err != nil {
		t.Fatal(err)
	}
	if !sawMove {
		t.Fatal("Email/set move was not sent")
	}
}
