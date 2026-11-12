package jmap

import (
	"context"
	"encoding/json"
	"fmt"
)

// MoveOutcome describes what the server said about a pending local move.
// Callers must not change the local tracking state as if a move succeeded
// when the message has been deleted or moved to a different mailbox remotely.
type MoveOutcome int

const (
	MoveCompleted MoveOutcome = iota
	MoveAlreadyInDestination
	MoveGone
	MoveMovedElsewhere
)

// MoveBetweenMailboxes preserves its existing error-only API. The syncer uses
// MoveBetweenMailboxesOutcome to distinguish successful moves from remote
// deletions and changes made on another device.
func (c *Client) MoveBetweenMailboxes(ctx context.Context, emailID, fromMailboxID, toMailboxID string) error {
	outcome, err := c.MoveBetweenMailboxesOutcome(ctx, emailID, fromMailboxID, toMailboxID)
	if err != nil {
		return err
	}
	if outcome == MoveMovedElsewhere {
		return fmt.Errorf("JMAP email %q is no longer in source mailbox", emailID)
	}
	return nil
}

// MoveBetweenMailboxesOutcome moves a JMAP Email without disturbing other
// mailbox memberships and reports why no move was necessary when remote state
// changed before the local change could be synchronized.
func (c *Client) MoveBetweenMailboxesOutcome(ctx context.Context, emailID, fromMailboxID, toMailboxID string) (MoveOutcome, error) {
	if fromMailboxID == toMailboxID {
		return MoveAlreadyInDestination, nil
	}

	emails, err := c.GetEmails(ctx, []string{emailID})
	if err != nil {
		return MoveCompleted, err
	}
	e, ok := emails[emailID]
	if !ok {
		return MoveGone, nil
	}
	if !e.MailboxIDs[fromMailboxID] {
		if e.MailboxIDs[toMailboxID] {
			return MoveAlreadyInDestination, nil
		}
		return MoveMovedElsewhere, nil
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
		return MoveCompleted, err
	}

	var r struct {
		NotUpdated map[string]setError `json:"notUpdated"`
	}
	if err := json.Unmarshal(payload, &r); err != nil {
		return MoveCompleted, err
	}
	if setErr, ok := r.NotUpdated[emailID]; ok {
		// The message may have been deleted between Email/get and Email/set.
		// Treat this like a vanished message, not a successful move.
		if setErr.Type == "notFound" {
			return MoveGone, nil
		}
		return MoveCompleted, setErr.asError("move")
	}
	return MoveCompleted, nil
}
