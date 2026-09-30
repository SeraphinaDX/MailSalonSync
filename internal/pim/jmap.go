package pim

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"git.cerberusgames.ca/Starstreak/MailSalonSync/internal/config"
	"git.cerberusgames.ca/Starstreak/MailSalonSync/internal/jmap"
)

type JMAPClient struct {
	client                                                         *jmap.Client
	capability, kind, groupKind, filter, membership, remote, state string
	objects                                                        map[string]Object
}

func (b *JMAPClient) Identity() string { return b.client.PIMIdentity() }

func NewJMAP(ctx context.Context, c config.Collection, secret string) (*JMAPClient, error) {
	b := &JMAPClient{capability: "urn:ietf:params:jmap:contacts", kind: "ContactCard", groupKind: "AddressBook", filter: "inAddressBook", membership: "addressBookIds", remote: c.Remote}
	if c.Protocol == "jmap-calendars" {
		b.capability = "urn:ietf:params:jmap:calendars"
		b.kind = "CalendarEvent"
		b.groupKind = "Calendar"
		b.filter = "inCalendar"
		b.membership = "calendarIds"
	}
	client, err := jmap.NewForCapability(ctx, c.JMAP.SessionURL, jmap.Auth{Mode: c.JMAP.Auth, Username: c.JMAP.Username, Secret: secret}, c.JMAP.AccountID, b.capability)
	if err != nil {
		return nil, err
	}
	b.client = client
	return b, nil
}

func (b *JMAPClient) call(ctx context.Context, method string, args map[string]any, out any) error {
	args["accountId"] = b.client.AccountID()
	data, err := b.client.CallPIM(ctx, b.capability, method, args)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}

func (b *JMAPClient) Discover(ctx context.Context) ([]Discovered, error) {
	var r struct {
		List []Discovered `json:"list"`
	}
	if err := b.call(ctx, b.groupKind+"/get", map[string]any{"ids": nil, "properties": []string{"id", "name"}}, &r); err != nil {
		return nil, err
	}
	return r.List, nil
}

type getResult struct {
	State    string            `json:"state"`
	List     []json.RawMessage `json:"list"`
	NotFound []string          `json:"notFound"`
}

func (b *JMAPClient) get(ctx context.Context, ids []string) (getResult, error) {
	var r getResult
	if err := b.call(ctx, b.kind+"/get", map[string]any{"ids": ids}, &r); err != nil {
		return r, err
	}
	if r.State == "" || len(r.NotFound) != 0 || len(r.List) != len(ids) {
		return r, errors.New("JMAP returned an incomplete object snapshot; rerun sync")
	}
	return r, nil
}

func (b *JMAPClient) Snapshot(ctx context.Context) ([]Object, error) {
	// Validate the mapped collection first. A missing group is not an empty one.
	var group struct {
		List     []json.RawMessage `json:"list"`
		NotFound []string          `json:"notFound"`
	}
	if err := b.call(ctx, b.groupKind+"/get", map[string]any{"ids": []string{b.remote}}, &group); err != nil {
		return nil, err
	}
	if len(group.List) != 1 || len(group.NotFound) != 0 {
		return nil, fmt.Errorf("JMAP %s %q does not exist or is inaccessible", b.groupKind, b.remote)
	}
	var groupValue struct {
		ID       string          `json:"id"`
		MyRights map[string]bool `json:"myRights"`
	}
	if err := json.Unmarshal(group.List[0], &groupValue); err != nil {
		return nil, err
	}
	right := "mayRead"
	if b.kind == "CalendarEvent" {
		right = "mayReadItems"
	}
	if groupValue.ID != b.remote || !groupValue.MyRights[right] {
		return nil, errors.New("JMAP mapped collection is not readable; refusing to infer deletions")
	}
	initial, err := b.get(ctx, []string{})
	if err != nil {
		return nil, err
	}
	b.state = initial.State
	b.objects = map[string]Object{}
	objects := []Object{}
	position := 0
	queryState := ""
	for {
		var q struct {
			IDs        []string `json:"ids"`
			QueryState string   `json:"queryState"`
			Position   int      `json:"position"`
		}
		if err := b.call(ctx, b.kind+"/query", map[string]any{"filter": map[string]string{b.filter: b.remote}, "position": position, "limit": 128}, &q); err != nil {
			return nil, err
		}
		if q.QueryState == "" || q.Position != position || (queryState != "" && q.QueryState != queryState) {
			return nil, errors.New("JMAP query changed during pagination; rerun sync")
		}
		queryState = q.QueryState
		if len(q.IDs) == 0 {
			break
		}
		// Small batches respect maxObjectsInGet even on constrained servers.
		for _, id := range q.IDs {
			r, err := b.get(ctx, []string{id})
			if err != nil {
				return nil, err
			}
			if r.State != b.state {
				return nil, errors.New("JMAP objects changed during snapshot; rerun sync")
			}
			var v map[string]any
			if err := json.Unmarshal(r.List[0], &v); err != nil {
				return nil, err
			}
			if v["id"] != id {
				return nil, errors.New("JMAP get returned the wrong object")
			}
			members, _ := v[b.membership].(map[string]any)
			if members[b.remote] != true {
				return nil, errors.New("JMAP object lacks mapped collection membership")
			}
			if _, duplicate := b.objects[id]; duplicate {
				return nil, errors.New("duplicate JMAP query ID")
			}
			data, _ := json.MarshalIndent(v, "", "  ")
			o := Object{ID: id, Revision: b.state, Data: data}
			b.objects[id] = o
			objects = append(objects, o)
		}
		position += len(q.IDs)
	}
	final, err := b.get(ctx, []string{})
	if err != nil {
		return nil, err
	}
	if final.State != b.state {
		return nil, errors.New("JMAP changed during snapshot; rerun sync")
	}
	return objects, nil
}

func pointer(s string) string { return strings.ReplaceAll(strings.ReplaceAll(s, "~", "~0"), "/", "~1") }
func readonly(k string) bool {
	switch k {
	case "id", "baseEventId", "isOrigin", "utcStart", "utcEnd":
		return true
	}
	return false
}

type setResult struct {
	NewState string `json:"newState"`
	Created  map[string]struct {
		ID string `json:"id"`
	} `json:"created"`
	Updated      map[string]json.RawMessage `json:"updated"`
	Destroyed    []string                   `json:"destroyed"`
	NotCreated   map[string]json.RawMessage `json:"notCreated"`
	NotUpdated   map[string]json.RawMessage `json:"notUpdated"`
	NotDestroyed map[string]json.RawMessage `json:"notDestroyed"`
}

func (b *JMAPClient) set(ctx context.Context, args map[string]any) (setResult, error) {
	var r setResult
	args["ifInState"] = b.state
	if b.kind == "CalendarEvent" {
		args["sendSchedulingMessages"] = false
	}
	if err := b.call(ctx, b.kind+"/set", args, &r); err != nil {
		return r, err
	}
	if len(r.NotCreated)+len(r.NotUpdated)+len(r.NotDestroyed) > 0 {
		data, _ := json.Marshal(r)
		return r, fmt.Errorf("JMAP set rejected: %s", data)
	}
	if r.NewState == "" {
		return r, errors.New("JMAP set returned no newState")
	}
	b.state = r.NewState
	return r, nil
}

func (b *JMAPClient) Put(ctx context.Context, id, revision string, data []byte) (Object, error) {
	if _, err := UID(data, ".json"); err != nil {
		return Object{}, err
	}
	var v map[string]any
	if err := json.Unmarshal(data, &v); err != nil {
		return Object{}, err
	}
	args := map[string]any{}
	if id == "" {
		for k := range v {
			if readonly(k) {
				delete(v, k)
			}
		}
		if v[b.membership] == nil {
			v[b.membership] = map[string]bool{b.remote: true}
		}
		membersJSON, _ := json.Marshal(v[b.membership])
		var members map[string]bool
		if json.Unmarshal(membersJSON, &members) != nil || !members[b.remote] {
			return Object{}, errors.New("new JMAP object must belong to mapped collection")
		}
		args["create"] = map[string]any{"new": v}
	} else {
		old, ok := b.objects[id]
		if !ok || old.Revision != revision {
			return Object{}, errors.New("unknown JMAP revision")
		}
		var original map[string]any
		if err := json.Unmarshal(old.Data, &original); err != nil {
			return Object{}, err
		}
		patch := map[string]any{}
		for k, value := range v {
			if readonly(k) {
				if !reflect.DeepEqual(value, original[k]) {
					return Object{}, fmt.Errorf("cannot change server-set JMAP property %s", k)
				}
				continue
			}
			if !reflect.DeepEqual(value, original[k]) {
				patch[pointer(k)] = value
			}
		}
		for k := range original {
			if _, ok := v[k]; !ok && !readonly(k) {
				patch[pointer(k)] = nil
			}
		}
		if !reflect.DeepEqual(v["uid"], original["uid"]) || !reflect.DeepEqual(v[b.membership], original[b.membership]) {
			return Object{}, errors.New("changing tracked UID or collection memberships is not supported")
		}
		args["update"] = map[string]any{id: patch}
	}
	r, err := b.set(ctx, args)
	if err != nil {
		return Object{}, err
	}
	if id == "" {
		id = r.Created["new"].ID
		if id == "" {
			return Object{}, errors.New("JMAP create returned no ID")
		}
	} else if _, ok := r.Updated[id]; !ok {
		return Object{}, errors.New("JMAP update was not acknowledged")
	}
	fetched, err := b.get(ctx, []string{id})
	if err != nil {
		return Object{}, err
	}
	if fetched.State != b.state {
		return Object{}, errors.New("JMAP changed immediately after write; rerun sync")
	}
	var vOut map[string]any
	if err := json.Unmarshal(fetched.List[0], &vOut); err != nil {
		return Object{}, err
	}
	if vOut["id"] != id {
		return Object{}, errors.New("JMAP get returned wrong ID after write")
	}
	canonical, _ := json.MarshalIndent(vOut, "", "  ")
	o := Object{ID: id, Revision: b.state, Data: canonical}
	b.objects[id] = o
	return o, nil
}

func (b *JMAPClient) Delete(ctx context.Context, o Object) error {
	var v map[string]any
	if err := json.Unmarshal(o.Data, &v); err != nil {
		return err
	}
	members, _ := v[b.membership].(map[string]any)
	count := 0
	for _, present := range members {
		if present == true {
			count++
		}
	}
	// Removing a local copy must not delete memberships in other address books
	// or calendars. Destroy only when this is the object's last membership.
	if count > 1 {
		r, err := b.set(ctx, map[string]any{"update": map[string]any{o.ID: map[string]any{pointer(b.membership) + "/" + pointer(b.remote): nil}}})
		if err != nil {
			return err
		}
		if _, ok := r.Updated[o.ID]; !ok {
			return errors.New("JMAP membership removal was not acknowledged")
		}
		return nil
	}
	r, err := b.set(ctx, map[string]any{"destroy": []string{o.ID}})
	if err != nil {
		return err
	}
	for _, id := range r.Destroyed {
		if id == o.ID {
			return nil
		}
	}
	return errors.New("JMAP deletion was not acknowledged")
}

func Open(ctx context.Context, c config.Collection) (Backend, error) {
	secret, err := c.Secret()
	if err != nil {
		return nil, err
	}
	if c.DAV != nil {
		return NewDAV(c, secret)
	}
	return NewJMAP(ctx, c, secret)
}
