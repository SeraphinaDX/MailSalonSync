package jmap

import (
	"context"
	"encoding/json"
	"fmt"
)

// MoveBetweenMailboxes moves a tracked JMAP Email from one mailbox to another
// without destroying the Email or disturbing any other mailbox memberships.
func (c *Client) MoveBetweenMailboxes(ctx context.Context, emailID, fromMailboxID, toMailboxID string) error {
	if fromMailboxID == toMailboxID {
		return nil
	}

	emails, err := c.GetEmails(ctx, []string{emailID})
	if err != nil {
		return err
	}
	e, ok := emails[emailID]
	if !ok {
		return fmt.Errorf("JMAP email %q no longer exists", emailID)
	}
	if !e.MailboxIDs[fromMailboxID] {
		if e.MailboxIDs[toMailboxID] {
			return nil
		}
		return fmt.Errorf("JMAP email %q is no longer in source mailbox", emailID)
	}

	payload, err := c.call(ctx, []string{capCore, capMail}, "Email/set", map[string]any{
		"accountId": c.accountID,
		"update": map[string]any{
			emailID: map[string]any{
				"mailboxIds/" + fromMailboxID: nil,
				"mailboxIds/" + toMailboxID:   true,
			},
		},
	})
	if err != nil {
		return err
	}

	var r struct {
		NotUpdated map[string]setError `json:"notUpdated"`
	}
	if err := json.Unmarshal(payload, &r); err != nil {
		return err
	}
	if e, ok := r.NotUpdated[emailID]; ok {
		return e.asError("move")
	}
	return nil
}
