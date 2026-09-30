package pim

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"git.cerberusgames.ca/Starstreak/MailSalonSync/internal/config"
)

func TestJMAPContactsAndCalendarRoundTrip(t *testing.T) {
	for _, protocol := range []string{"jmap-contacts", "jmap-calendars"} {
		t.Run(protocol, func(t *testing.T) {
			capability, kind, group, membership, readRight := "urn:ietf:params:jmap:contacts", "ContactCard", "AddressBook", "addressBookIds", "mayRead"
			if protocol == "jmap-calendars" {
				capability, kind, group, membership, readRight = "urn:ietf:params:jmap:calendars", "CalendarEvent", "Calendar", "calendarIds", "mayReadItems"
			}
			objects := map[string]map[string]any{"a": {"id": "a", "uid": "ua", "x-unknown": map[string]any{"nested": true}, membership: map[string]any{"book": true, "other": true}}, "b": {"id": "b", "uid": "ub", membership: map[string]any{"book": true}}}
			state := 1
			changedDuringSnapshot := false
			denyRead := false
			sawPatch, sawUnlink, sawDestroy := false, false, false
			var srv *httptest.Server
			srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				user, secret, ok := r.BasicAuth()
				if !ok || user != "u" || secret != "s" {
					t.Error("missing authentication")
					w.WriteHeader(401)
					return
				}
				if r.URL.Path == "/session" {
					json.NewEncoder(w).Encode(map[string]any{"apiUrl": srv.URL + "/api", "capabilities": map[string]any{"urn:ietf:params:jmap:core": map[string]any{}, capability: map[string]any{}}, "primaryAccounts": map[string]string{capability: "pim-account"}, "accounts": map[string]any{"pim-account": map[string]any{"accountCapabilities": map[string]any{capability: map[string]any{}}}}})
					return
				}
				var req struct {
					Using       []string            `json:"using"`
					MethodCalls [][]json.RawMessage `json:"methodCalls"`
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Fatal(err)
				}
				if len(req.Using) != 2 || req.Using[1] != capability {
					t.Error("wrong capability")
				}
				var method string
				json.Unmarshal(req.MethodCalls[0][0], &method)
				args := map[string]any{}
				json.Unmarshal(req.MethodCalls[0][1], &args)
				if args["accountId"] != "pim-account" {
					t.Error("used wrong primary account")
				}
				result := map[string]any{}
				switch method {
				case group + "/get":
					result = map[string]any{"list": []any{map[string]any{"id": "book", "name": "Personal", "myRights": map[string]bool{readRight: !denyRead}}}, "notFound": []any{}}
				case kind + "/query":
					filter := args["filter"].(map[string]any)
					if filter["in"+group] != "book" {
						t.Error("wrong collection filter")
					}
					position := int(args["position"].(float64))
					ids := []string{}
					if position == 0 {
						ids = []string{"a"}
					} else if position == 1 {
						ids = []string{"b"}
					}
					result = map[string]any{"ids": ids, "queryState": "q1", "position": position}
				case kind + "/get":
					list := []any{}
					missing := []string{}
					for _, value := range args["ids"].([]any) {
						id := value.(string)
						if o, ok := objects[id]; ok {
							list = append(list, o)
						} else {
							missing = append(missing, id)
						}
					}
					if changedDuringSnapshot && len(list) > 0 {
						state++
					}
					result = map[string]any{"list": list, "notFound": missing, "state": fmt.Sprint(state)}
				case kind + "/set":
					if args["ifInState"] != fmt.Sprint(state) {
						method = "error"
						result = map[string]any{"type": "stateMismatch"}
						break
					}
					if protocol == "jmap-calendars" && args["sendSchedulingMessages"] != false {
						t.Error("calendar sync enabled scheduling messages")
					}
					state++
					result["newState"] = fmt.Sprint(state)
					if creates, ok := args["create"].(map[string]any); ok {
						o := creates["new"].(map[string]any)
						if _, hasID := o["id"]; hasID {
							t.Error("sent server ID on create")
						}
						o["id"] = "newID"
						objects["newID"] = o
						result["created"] = map[string]any{"new": map[string]string{"id": "newID"}}
					}
					if updates, ok := args["update"].(map[string]any); ok {
						ack := map[string]any{}
						for id, value := range updates {
							patch := value.(map[string]any)
							o := objects[id]
							if _, exists := patch["id"]; exists {
								t.Error("sent immutable ID patch")
							}
							if _, exists := patch["x-unknown"]; exists {
								t.Error("rewrote unchanged extension")
							}
							for key, v := range patch {
								if key == membership+"/book" {
									delete(o[membership].(map[string]any), "book")
									sawUnlink = true
								} else {
									o[key] = v
									sawPatch = true
								}
							}
							ack[id] = nil
						}
						result["updated"] = ack
					}
					if ids, ok := args["destroy"].([]any); ok {
						for _, id := range ids {
							delete(objects, id.(string))
						}
						result["destroyed"] = ids
						sawDestroy = true
					}
				default:
					t.Errorf("unexpected method %s", method)
				}
				json.NewEncoder(w).Encode(map[string]any{"methodResponses": []any{[]any{method, result, "0"}}})
			}))
			defer srv.Close()
			oldTransport := http.DefaultTransport
			http.DefaultTransport = srv.Client().Transport
			defer func() { http.DefaultTransport = oldTransport }()
			c := config.Collection{Protocol: protocol, Remote: "book", JMAP: &config.JMAP{SessionURL: srv.URL + "/session", Auth: "basic", Username: "u"}}
			ctx := context.Background()
			b, err := NewJMAP(ctx, c, "s")
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(b.Identity(), "pim-account") {
				t.Fatal("identity lacks actual account")
			}
			groups, err := b.Discover(ctx)
			if err != nil || len(groups) != 1 {
				t.Fatalf("discovery: %v %+v", err, groups)
			}
			items, err := b.Snapshot(ctx)
			if err != nil || len(items) != 2 {
				t.Fatalf("pagination: %v %+v", err, items)
			}
			var edited map[string]any
			json.Unmarshal(b.objects["a"].Data, &edited)
			edited["title"] = "Edited"
			data, _ := json.Marshal(edited)
			updated, err := b.Put(ctx, "a", b.objects["a"].Revision, data)
			if err != nil {
				t.Fatal(err)
			}
			if !sawPatch {
				t.Fatal("did not send patch")
			}
			if err := b.Delete(ctx, updated); err != nil {
				t.Fatal(err)
			}
			if !sawUnlink || objects["a"][membership].(map[string]any)["other"] != true {
				t.Fatal("lost other collection membership")
			}
			if err := b.Delete(ctx, b.objects["b"]); err != nil || !sawDestroy {
				t.Fatalf("destroy: %v", err)
			}
			created, err := b.Put(ctx, "", "", []byte(`{"uid":"newuid","title":"New"}`))
			if err != nil || created.ID != "newID" {
				t.Fatalf("create: %v %+v", err, created)
			}
			// Restore snapshot objects to exercise consistency and access failures.
			objects["a"][membership].(map[string]any)["book"] = true
			objects["b"] = map[string]any{"id": "b", "uid": "ub", membership: map[string]any{"book": true}}
			changedDuringSnapshot = true
			if _, err := b.Snapshot(ctx); err == nil {
				t.Fatal("accepted changing snapshot")
			}
			changedDuringSnapshot = false
			denyRead = true
			if _, err := b.Snapshot(ctx); err == nil {
				t.Fatal("accepted unreadable collection")
			}
		})
	}
}
