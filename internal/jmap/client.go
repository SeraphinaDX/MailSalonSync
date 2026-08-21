package jmap

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/mail"
	"net/url"
	"sort"
	"strings"
	"time"
)

const (
	capCore       = "urn:ietf:params:jmap:core"
	capMail       = "urn:ietf:params:jmap:mail"
	capSubmission = "urn:ietf:params:jmap:submission"
)

type Auth struct {
	Mode     string
	Username string
	Secret   string
}

type Session struct {
	Capabilities    map[string]json.RawMessage `json:"capabilities"`
	PrimaryAccounts map[string]string          `json:"primaryAccounts"`
	APIURL          string                     `json:"apiUrl"`
	DownloadURL     string                     `json:"downloadUrl"`
	UploadURL       string                     `json:"uploadUrl"`
	Username        string                     `json:"username"`
}

type Client struct {
	http      *http.Client
	auth      Auth
	session   Session
	accountID string
}

type Mailbox struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	ParentID *string `json:"parentId"`
	Role     *string `json:"role"`
	FullName string  `json:"-"`
}

type Email struct {
	ID         string          `json:"id"`
	BlobID     string          `json:"blobId"`
	MailboxIDs map[string]bool `json:"mailboxIds"`
	Keywords   map[string]bool `json:"keywords"`
}

type Identity struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

func New(ctx context.Context, sessionURL string, auth Auth, accountID string) (*Client, error) {
	if err := requireHTTPSURL(sessionURL, "JMAP session URL"); err != nil {
		return nil, err
	}
	c := &Client{auth: auth}
	c.http = &http.Client{
		Timeout: 90 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if !strings.EqualFold(req.URL.Scheme, "https") {
				return errors.New("refusing JMAP redirect to non-HTTPS URL")
			}
			return nil
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sessionURL, nil)
	if err != nil {
		return nil, err
	}
	c.authorize(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("JMAP session: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("JMAP session HTTP %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	if err := json.NewDecoder(resp.Body).Decode(&c.session); err != nil {
		return nil, fmt.Errorf("decode JMAP session: %w", err)
	}
	for label, endpoint := range map[string]string{
		"JMAP API URL":      c.session.APIURL,
		"JMAP download URL": c.session.DownloadURL,
		"JMAP upload URL":   c.session.UploadURL,
	} {
		if err := requireHTTPSURL(endpoint, label); err != nil {
			return nil, err
		}
	}
	if _, ok := c.session.Capabilities[capMail]; !ok {
		return nil, errors.New("server does not advertise JMAP Mail")
	}
	if accountID == "" {
		accountID = c.session.PrimaryAccounts[capMail]
	}
	if accountID == "" {
		return nil, errors.New("JMAP session has no primary mail account; set account_id")
	}
	c.accountID = accountID
	return c, nil
}

func requireHTTPSURL(raw, label string) error {
	u, err := url.Parse(raw)
	if err != nil || !u.IsAbs() || !strings.EqualFold(u.Scheme, "https") || u.Host == "" {
		return fmt.Errorf("%s must be an absolute https URL", label)
	}
	return nil
}

func (c *Client) AccountID() string { return c.accountID }

func (c *Client) authorize(req *http.Request) {
	if c.auth.Mode == "bearer" {
		req.Header.Set("Authorization", "Bearer "+c.auth.Secret)
		return
	}
	cred := base64.StdEncoding.EncodeToString([]byte(c.auth.Username + ":" + c.auth.Secret))
	req.Header.Set("Authorization", "Basic "+cred)
}

func (c *Client) call(ctx context.Context, using []string, method string, args any) (json.RawMessage, error) {
	body := map[string]any{
		"using":       using,
		"methodCalls": []any{[]any{method, args, "0"}},
	}
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.session.APIURL, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	c.authorize(req)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		return nil, fmt.Errorf("JMAP API HTTP %s: %s", resp.Status, strings.TrimSpace(string(data)))
	}
	var out struct {
		MethodResponses [][]json.RawMessage `json:"methodResponses"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode JMAP response: %w", err)
	}
	if len(out.MethodResponses) == 0 || len(out.MethodResponses[0]) < 3 {
		return nil, errors.New("JMAP response contains no method response")
	}
	var name string
	if err := json.Unmarshal(out.MethodResponses[0][0], &name); err != nil {
		return nil, err
	}
	payload := out.MethodResponses[0][1]
	if name == "error" {
		var e struct {
			Type        string `json:"type"`
			Description string `json:"description"`
		}
		_ = json.Unmarshal(payload, &e)
		if e.Description != "" {
			return nil, fmt.Errorf("JMAP %s: %s", e.Type, e.Description)
		}
		return nil, fmt.Errorf("JMAP method error: %s", e.Type)
	}
	if name != method {
		return nil, fmt.Errorf("JMAP response method %q, expected %q", name, method)
	}
	return payload, nil
}

func (c *Client) ListMailboxes(ctx context.Context) ([]Mailbox, error) {
	payload, err := c.call(ctx, []string{capCore, capMail}, "Mailbox/get", map[string]any{
		"accountId":  c.accountID,
		"ids":        nil,
		"properties": []string{"id", "name", "parentId", "role"},
	})
	if err != nil {
		return nil, err
	}
	var r struct {
		List []Mailbox `json:"list"`
	}
	if err := json.Unmarshal(payload, &r); err != nil {
		return nil, err
	}
	byID := make(map[string]*Mailbox, len(r.List))
	for i := range r.List {
		byID[r.List[i].ID] = &r.List[i]
	}
	var fullName func(*Mailbox, map[string]bool) string
	fullName = func(m *Mailbox, visiting map[string]bool) string {
		if m.FullName != "" {
			return m.FullName
		}
		if visiting[m.ID] {
			return m.Name
		}
		visiting[m.ID] = true
		if m.ParentID != nil {
			if p := byID[*m.ParentID]; p != nil {
				m.FullName = fullName(p, visiting) + "/" + m.Name
			}
		}
		if m.FullName == "" {
			m.FullName = m.Name
		}
		delete(visiting, m.ID)
		return m.FullName
	}
	for i := range r.List {
		fullName(&r.List[i], map[string]bool{})
	}
	sort.Slice(r.List, func(i, j int) bool { return r.List[i].FullName < r.List[j].FullName })
	return r.List, nil
}

func ResolveMailbox(spec string, boxes []Mailbox) (Mailbox, error) {
	if strings.HasPrefix(spec, "id:") {
		id := strings.TrimPrefix(spec, "id:")
		for _, b := range boxes {
			if b.ID == id {
				return b, nil
			}
		}
		return Mailbox{}, fmt.Errorf("no JMAP mailbox with id %q", id)
	}
	if strings.HasPrefix(spec, "role:") {
		role := strings.ToLower(strings.TrimPrefix(spec, "role:"))
		for _, b := range boxes {
			if b.Role != nil && strings.EqualFold(*b.Role, role) {
				return b, nil
			}
		}
		return Mailbox{}, fmt.Errorf("no JMAP mailbox with role %q", role)
	}
	for _, b := range boxes {
		if b.FullName == spec {
			return b, nil
		}
	}
	var matches []Mailbox
	for _, b := range boxes {
		if strings.EqualFold(b.FullName, spec) || strings.EqualFold(b.Name, spec) {
			matches = append(matches, b)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		return Mailbox{}, fmt.Errorf("JMAP mailbox %q is ambiguous; use its full path, role:, or id:", spec)
	}
	return Mailbox{}, fmt.Errorf("no JMAP mailbox matching %q", spec)
}

func (c *Client) QueryEmailIDs(ctx context.Context, mailboxID string) ([]string, error) {
	var all []string
	position := 0
	for {
		payload, err := c.call(ctx, []string{capCore, capMail}, "Email/query", map[string]any{
			"accountId":      c.accountID,
			"filter":         map[string]any{"inMailbox": mailboxID},
			"sort":           []any{map[string]any{"property": "receivedAt", "isAscending": true}},
			"position":       position,
			"limit":          500,
			"calculateTotal": true,
		})
		if err != nil {
			return nil, err
		}
		var r struct {
			IDs   []string `json:"ids"`
			Total int      `json:"total"`
		}
		if err := json.Unmarshal(payload, &r); err != nil {
			return nil, err
		}
		all = append(all, r.IDs...)
		position += len(r.IDs)
		if len(r.IDs) == 0 || position >= r.Total {
			return all, nil
		}
	}
}

func (c *Client) GetEmails(ctx context.Context, ids []string) (map[string]Email, error) {
	result := make(map[string]Email, len(ids))
	for start := 0; start < len(ids); start += 200 {
		end := start + 200
		if end > len(ids) {
			end = len(ids)
		}
		payload, err := c.call(ctx, []string{capCore, capMail}, "Email/get", map[string]any{
			"accountId":  c.accountID,
			"ids":        ids[start:end],
			"properties": []string{"id", "blobId", "mailboxIds", "keywords"},
		})
		if err != nil {
			return nil, err
		}
		var r struct {
			List []Email `json:"list"`
		}
		if err := json.Unmarshal(payload, &r); err != nil {
			return nil, err
		}
		for _, e := range r.List {
			result[e.ID] = e
		}
	}
	return result, nil
}

func (c *Client) DownloadEmail(ctx context.Context, blobID string) ([]byte, error) {
	u := expandTemplate(c.session.DownloadURL, map[string]string{
		"accountId": c.accountID,
		"blobId":    blobID,
		"type":      "message/rfc822",
		"name":      "message.eml",
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	c.authorize(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("JMAP download HTTP %s: %s", resp.Status, strings.TrimSpace(string(data)))
	}
	return io.ReadAll(resp.Body)
}

func (c *Client) RemoveFromMailbox(ctx context.Context, emailID, mailboxID string) error {
	emails, err := c.GetEmails(ctx, []string{emailID})
	if err != nil {
		return err
	}
	e, ok := emails[emailID]
	if !ok {
		return nil
	}
	if !e.MailboxIDs[mailboxID] {
		return nil
	}
	args := map[string]any{"accountId": c.accountID}
	if len(e.MailboxIDs) <= 1 {
		args["destroy"] = []string{emailID}
	} else {
		args["update"] = map[string]any{
			emailID: map[string]any{"mailboxIds/" + mailboxID: nil},
		}
	}
	payload, err := c.call(ctx, []string{capCore, capMail}, "Email/set", args)
	if err != nil {
		return err
	}
	var r struct {
		NotUpdated   map[string]setError `json:"notUpdated"`
		NotDestroyed map[string]setError `json:"notDestroyed"`
	}
	_ = json.Unmarshal(payload, &r)
	if e, ok := r.NotUpdated[emailID]; ok {
		return e.asError("update")
	}
	if e, ok := r.NotDestroyed[emailID]; ok {
		if e.Type == "notFound" {
			return nil
		}
		return e.asError("destroy")
	}
	return nil
}

type setError struct {
	Type        string `json:"type"`
	Description string `json:"description"`
}

func (e setError) asError(action string) error {
	if e.Description != "" {
		return fmt.Errorf("JMAP %s %s: %s", action, e.Type, e.Description)
	}
	return fmt.Errorf("JMAP %s: %s", action, e.Type)
}

func (c *Client) SendRaw(ctx context.Context, raw []byte, configuredIdentity, draftsSpec, sentSpec string) (string, error) {
	if _, ok := c.session.Capabilities[capSubmission]; !ok {
		return "", errors.New("server does not advertise JMAP Submission")
	}
	boxes, err := c.ListMailboxes(ctx)
	if err != nil {
		return "", err
	}
	drafts, err := ResolveMailbox(draftsSpec, boxes)
	if err != nil {
		return "", fmt.Errorf("resolve drafts mailbox: %w", err)
	}
	var sent *Mailbox
	if sentSpec != "" {
		if b, err := ResolveMailbox(sentSpec, boxes); err == nil {
			sent = &b
		}
	}
	blobID, err := c.upload(ctx, raw)
	if err != nil {
		return "", err
	}
	payload, err := c.call(ctx, []string{capCore, capMail}, "Email/import", map[string]any{
		"accountId": c.accountID,
		"emails": map[string]any{
			"draft": map[string]any{
				"blobId":     blobID,
				"mailboxIds": map[string]bool{drafts.ID: true},
				"keywords":   map[string]bool{"$draft": true},
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
	if e, ok := imported.NotCreated["draft"]; ok {
		return "", e.asError("import")
	}
	emailID := imported.Created["draft"].ID
	if emailID == "" {
		return "", errors.New("JMAP Email/import returned no email id")
	}
	identityID, err := c.chooseIdentity(ctx, raw, configuredIdentity)
	if err != nil {
		return emailID, err
	}
	patch := map[string]any{"keywords/$draft": nil}
	if sent != nil {
		if sent.ID != drafts.ID {
			patch["mailboxIds/"+drafts.ID] = nil
		}
		patch["mailboxIds/"+sent.ID] = true
	}
	payload, err = c.call(ctx, []string{capCore, capMail, capSubmission}, "EmailSubmission/set", map[string]any{
		"accountId": c.accountID,
		"create": map[string]any{
			"send": map[string]any{
				"identityId": identityID,
				"emailId":    emailID,
			},
		},
		"onSuccessUpdateEmail": map[string]any{"#send": patch},
	})
	if err != nil {
		return emailID, err
	}
	var submitted struct {
		Created map[string]struct {
			ID string `json:"id"`
		} `json:"created"`
		NotCreated map[string]setError `json:"notCreated"`
	}
	if err := json.Unmarshal(payload, &submitted); err != nil {
		return emailID, err
	}
	if e, ok := submitted.NotCreated["send"]; ok {
		return emailID, e.asError("submit")
	}
	if submitted.Created["send"].ID == "" {
		return emailID, errors.New("JMAP submission returned no submission id")
	}
	return emailID, nil
}

func (c *Client) upload(ctx context.Context, raw []byte) (string, error) {
	u := expandTemplate(c.session.UploadURL, map[string]string{"accountId": c.accountID})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	c.authorize(req)
	req.Header.Set("Content-Type", "message/rfc822")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("JMAP upload HTTP %s: %s", resp.Status, strings.TrimSpace(string(data)))
	}
	var out struct {
		BlobID string `json:"blobId"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if out.BlobID == "" {
		return "", errors.New("JMAP upload returned no blobId")
	}
	return out.BlobID, nil
}

func (c *Client) chooseIdentity(ctx context.Context, raw []byte, configured string) (string, error) {
	if configured != "" {
		return configured, nil
	}
	payload, err := c.call(ctx, []string{capCore, capMail, capSubmission}, "Identity/get", map[string]any{
		"accountId": c.accountID,
		"ids":       nil,
	})
	if err != nil {
		return "", err
	}
	var r struct {
		List []Identity `json:"list"`
	}
	if err := json.Unmarshal(payload, &r); err != nil {
		return "", err
	}
	if len(r.List) == 0 {
		return "", errors.New("JMAP account has no sending identities")
	}
	from := fromAddress(raw)
	if from != "" {
		for _, id := range r.List {
			if strings.EqualFold(id.Email, from) {
				return id.ID, nil
			}
			if strings.HasPrefix(id.Email, "*@") && strings.HasSuffix(strings.ToLower(from), strings.ToLower(strings.TrimPrefix(id.Email, "*"))) {
				return id.ID, nil
			}
		}
	}
	return r.List[0].ID, nil
}

func fromAddress(raw []byte) string {
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return ""
	}
	a, err := mail.ParseAddress(msg.Header.Get("From"))
	if err != nil {
		return ""
	}
	return a.Address
}

func expandTemplate(template string, values map[string]string) string {
	out := template
	for k, v := range values {
		out = strings.ReplaceAll(out, "{"+k+"}", url.PathEscape(v))
	}
	return out
}
