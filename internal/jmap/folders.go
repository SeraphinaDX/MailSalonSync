package jmap

import (
	"context"
	"encoding/json"
	"fmt"
)

func (c *Client) CreateMailbox(ctx context.Context, name string, parentID *string) (string, error) {
	payload, err := c.call(ctx, []string{capCore, capMail}, "Mailbox/set", map[string]any{"accountId": c.accountID, "create": map[string]any{"folder": map[string]any{"name": name, "parentId": parentID, "isSubscribed": true}}})
	if err != nil {
		return "", err
	}
	var r struct {
		Created map[string]struct {
			ID string `json:"id"`
		} `json:"created"`
		NotCreated map[string]setError `json:"notCreated"`
	}
	if err = json.Unmarshal(payload, &r); err != nil {
		return "", err
	}
	if e, ok := r.NotCreated["folder"]; ok {
		return "", e.asError("create mailbox")
	}
	if r.Created["folder"].ID == "" {
		return "", fmt.Errorf("Mailbox/set did not confirm creation")
	}
	return r.Created["folder"].ID, nil
}
func (c *Client) SetSubscribed(ctx context.Context, id string, subscribed bool) error {
	payload, err := c.call(ctx, []string{capCore, capMail}, "Mailbox/set", map[string]any{"accountId": c.accountID, "update": map[string]any{id: map[string]any{"isSubscribed": subscribed}}})
	if err != nil {
		return err
	}
	var r struct {
		Updated    map[string]json.RawMessage `json:"updated"`
		NotUpdated map[string]setError        `json:"notUpdated"`
	}
	if err = json.Unmarshal(payload, &r); err != nil {
		return err
	}
	if e, ok := r.NotUpdated[id]; ok {
		return e.asError("subscribe mailbox")
	}
	if _, ok := r.Updated[id]; !ok {
		return fmt.Errorf("Mailbox/set did not confirm subscription update")
	}
	return nil
}
