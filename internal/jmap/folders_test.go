package jmap

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestJMAPFolderMutationsAndSetErrors(t *testing.T) {
	for _, scenario := range []string{"success", "notCreated", "notUpdated", "missing confirmation"} {
		t.Run(scenario, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req struct {
					MethodCalls [][]json.RawMessage `json:"methodCalls"`
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
					return
				}
				var method string
				json.Unmarshal(req.MethodCalls[0][0], &method)
				if method != "Mailbox/set" {
					t.Error(method)
				}
				var args map[string]json.RawMessage
				json.Unmarshal(req.MethodCalls[0][1], &args)
				payload := `{"updated":{"new":null}}`
				if calls == 0 {
					var created map[string]struct {
						Name       string `json:"name"`
						ParentID   string `json:"parentId"`
						Subscribed bool   `json:"isSubscribed"`
					}
					json.Unmarshal(args["create"], &created)
					if created["folder"].Name != "旅行" || created["folder"].ParentID != "parent" || !created["folder"].Subscribed {
						t.Error(created)
					}
					payload = `{"created":{"folder":{"id":"new"}}}`
					if scenario == "notCreated" {
						payload = `{"notCreated":{"folder":{"type":"forbidden","description":"No create permission"}}}`
					}
				} else {
					var updates map[string]map[string]bool
					json.Unmarshal(args["update"], &updates)
					if updates["new"]["isSubscribed"] {
						t.Error("expected unsubscribe")
					}
					if scenario == "notUpdated" {
						payload = `{"notUpdated":{"new":{"type":"forbidden"}}}`
					}
				}
				if scenario == "missing confirmation" {
					payload = `{}`
				}
				calls++
				fmt.Fprintf(w, `{"methodResponses":[["Mailbox/set",%s,"0"]]}`, payload)
			}))
			defer srv.Close()
			c := &Client{http: srv.Client(), session: Session{APIURL: srv.URL}, accountID: "account"}
			parent := "parent"
			id, err := c.CreateMailbox(context.Background(), "旅行", &parent)
			if scenario == "notCreated" || scenario == "missing confirmation" {
				if err == nil {
					t.Fatal("unconfirmed create accepted")
				}
				return
			}
			if err != nil || id != "new" {
				t.Fatal(id, err)
			}
			err = c.SetSubscribed(context.Background(), id, false)
			if (err != nil) != (scenario == "notUpdated") {
				t.Fatal(err)
			}
		})
	}
}
