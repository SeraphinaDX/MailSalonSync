package jmap

import (
	"context"
	"encoding/json"
	"errors"
)

// ImportRaw uploads an RFC 5322 message and imports it into the requested
// mailbox without submitting/sending it. This is intended for one-shot
// migration of pre-existing local Maildir messages.
func (c *Client) ImportRaw(ctx context.Context, raw []byte, mailboxID string, seen bool) (string, error) {
	if mailboxID == "" {
		return "", errors.New("JMAP import requires a mailbox id")
	}
	blobID, err := c.upload(ctx, raw)
	if err != nil {
		return "", err
	}
	keywords := map[string]bool{}
	if seen {
		keywords["$seen"] = true
	}
	payload, err := c.call(ctx, []string{capCore, capMail}, "Email/import", map[string]any{
		"accountId": c.accountID,
		"emails": map[string]any{
			"message": map[string]any{
				"blobId":     blobID,
				"mailboxIds": map[string]bool{mailboxID: true},
				"keywords":   keywords,
			},
		},
	})
	if err != nil {
		return "", err
	}
	var imported struct {
		Created map[string]struct {
			ID string `json:"id"`
		} `json:"created"`
		NotCreated map[string]setError `json:"notCreated"`
	}
	if err := json.Unmarshal(payload, &imported); err != nil {
		return "", err
	}
	if e, ok := imported.NotCreated["message"]; ok {
		return "", e.asError("import")
	}
	emailID := imported.Created["message"].ID
	if emailID == "" {
		return "", errors.New("JMAP Email/import returned no email id")
	}
	return emailID, nil
}
