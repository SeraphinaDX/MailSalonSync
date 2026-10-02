package imapclient

import (
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

type Mailbox struct {
	Name           string
	Delimiter      string
	Subscribed     bool
	Selectable     bool
	CanCreateChild bool
}

// LIST is authoritative for existence; LSUB may contain deleted names, so it
// only contributes subscription state to folders returned by LIST.
func (c *Client) ListMailboxes() ([]Mailbox, error) {
	var rows []Mailbox
	parse := func(line string) error {
		if !strings.HasPrefix(strings.ToUpper(line), "* LIST ") {
			return nil
		}
		rest := strings.TrimSpace(line[len("* LIST "):])
		if !strings.HasPrefix(rest, "(") {
			return fmt.Errorf("malformed LIST response")
		}
		end := strings.Index(rest, ")")
		if end < 0 {
			return fmt.Errorf("malformed LIST flags")
		}
		flags := map[string]bool{}
		for _, flag := range strings.Fields(rest[1:end]) {
			flags[strings.ToUpper(flag)] = true
		}
		rest = rest[end+1:]
		delimiter, err := c.listString(&rest)
		if err != nil {
			return err
		}
		name, err := c.listString(&rest)
		if err != nil {
			return err
		}
		name, err = c.decodeMailbox(name)
		if err != nil {
			return err
		}
		rows = append(rows, Mailbox{Name: name, Delimiter: delimiter, Subscribed: flags[`\SUBSCRIBED`], Selectable: !flags[`\NOSELECT`] && !flags[`\NONEXISTENT`], CanCreateChild: delimiter != "" && !flags[`\NOINFERIORS`]})
		return nil
	}
	command := `LIST "" "*"`
	if c.HasCapability("IMAP4REV2") {
		command += " RETURN (SUBSCRIBED)"
	}
	if _, err := c.simple(command, parse); err != nil {
		return nil, err
	}
	if c.HasCapability("IMAP4REV2") {
		return rows, nil
	}
	subscribed := map[string]bool{}
	_, err := c.simple(`LSUB "" "*"`, func(line string) error {
		if !strings.HasPrefix(strings.ToUpper(line), "* LSUB ") {
			return nil
		}
		rest := strings.TrimSpace(line[len("* LSUB "):])
		end := strings.Index(rest, ")")
		if !strings.HasPrefix(rest, "(") || end < 0 {
			return fmt.Errorf("malformed LSUB response")
		}
		rest = rest[end+1:]
		if _, err := c.listString(&rest); err != nil {
			return err
		}
		name, err := c.listString(&rest)
		if err != nil {
			return err
		}
		name, err = c.decodeMailbox(name)
		if err != nil {
			return err
		}
		subscribed[name] = true
		return nil
	})
	if err != nil {
		return nil, err
	}
	for i := range rows {
		rows[i].Subscribed = subscribed[rows[i].Name]
	}
	return rows, nil
}

// Parse quoted strings, atoms and literals, consuming the rest of a literal's
// response line so tagged command completion cannot be mistaken for a name.
func (c *Client) listString(rest *string) (string, error) {
	s := strings.TrimSpace(*rest)
	if s == "" {
		return "", fmt.Errorf("missing LIST string")
	}
	if s[0] == '"' {
		var b strings.Builder
		for i := 1; i < len(s); i++ {
			if s[i] == '"' {
				*rest = s[i+1:]
				return b.String(), nil
			}
			if s[i] == '\\' {
				i++
				if i >= len(s) {
					break
				}
			}
			b.WriteByte(s[i])
		}
		return "", fmt.Errorf("unterminated LIST string")
	}
	if s[0] == '{' {
		end := strings.Index(s, "}")
		if end < 0 {
			return "", fmt.Errorf("invalid LIST literal")
		}
		n, err := strconv.Atoi(strings.TrimSuffix(s[1:end], "+"))
		if err != nil || n < 0 || n > 1024*1024 {
			return "", fmt.Errorf("invalid LIST literal size")
		}
		b := make([]byte, n)
		if _, err = io.ReadFull(c.r, b); err != nil {
			return "", err
		}
		tail, err := c.readLine()
		if err != nil {
			return "", err
		}
		*rest = tail
		return string(b), nil
	}
	end := strings.IndexAny(s, " \t")
	if end < 0 {
		end = len(s)
	}
	*rest = s[end:]
	if strings.EqualFold(s[:end], "NIL") {
		return "", nil
	}
	return s[:end], nil
}
func (c *Client) CreateMailbox(name string) error {
	if err := validMailboxName(name); err != nil {
		return err
	}
	_, err := c.simple("CREATE "+quote(c.encodeMailbox(name)), nil)
	return err
}
func (c *Client) SetSubscribed(name string, subscribed bool) error {
	if err := validMailboxName(name); err != nil {
		return err
	}
	verb := "UNSUBSCRIBE"
	if subscribed {
		verb = "SUBSCRIBE"
	}
	_, err := c.simple(verb+" "+quote(c.encodeMailbox(name)), nil)
	return err
}
func validMailboxName(name string) error {
	if name == "" || !utf8.ValidString(name) || strings.ContainsAny(name, "\x00\r\n") {
		return fmt.Errorf("invalid mailbox name")
	}
	return nil
}
func (c *Client) encodeMailbox(name string) string {
	if c.HasCapability("IMAP4REV2") {
		return name
	}
	return encodeUTF7(name)
}
func (c *Client) decodeMailbox(name string) (string, error) {
	if c.HasCapability("IMAP4REV2") {
		if !utf8.ValidString(name) {
			return "", fmt.Errorf("invalid UTF-8 mailbox")
		}
		return name, nil
	}
	return decodeUTF7(name)
}

// IMAP4rev1 uses modified UTF-7 until UTF8=ACCEPT is explicitly enabled.
func encodeUTF7(s string) string {
	var out strings.Builder
	var run []rune
	flush := func() {
		if len(run) == 0 {
			return
		}
		units := utf16.Encode(run)
		b := make([]byte, len(units)*2)
		for i, u := range units {
			binary.BigEndian.PutUint16(b[i*2:], u)
		}
		out.WriteByte('&')
		out.WriteString(strings.ReplaceAll(base64.RawStdEncoding.EncodeToString(b), "/", ","))
		out.WriteByte('-')
		run = nil
	}
	for _, r := range s {
		if r >= 0x20 && r <= 0x7e {
			flush()
			if r == '&' {
				out.WriteString("&-")
			} else {
				out.WriteRune(r)
			}
		} else {
			run = append(run, r)
		}
	}
	flush()
	return out.String()
}
func decodeUTF7(s string) (string, error) {
	var out strings.Builder
	for len(s) > 0 {
		i := strings.IndexByte(s, '&')
		if i < 0 {
			out.WriteString(s)
			break
		}
		out.WriteString(s[:i])
		s = s[i+1:]
		end := strings.IndexByte(s, '-')
		if end < 0 {
			return "", fmt.Errorf("invalid modified UTF-7 mailbox")
		}
		if end == 0 {
			out.WriteByte('&')
		} else {
			b, err := base64.RawStdEncoding.DecodeString(strings.ReplaceAll(s[:end], ",", "/"))
			if err != nil || len(b)%2 != 0 {
				return "", fmt.Errorf("invalid modified UTF-7 mailbox")
			}
			units := make([]uint16, len(b)/2)
			for i := range units {
				units[i] = binary.BigEndian.Uint16(b[i*2:])
			}
			for i := 0; i < len(units); i++ {
				if units[i] >= 0xd800 && units[i] <= 0xdbff {
					if i+1 >= len(units) || units[i+1] < 0xdc00 || units[i+1] > 0xdfff {
						return "", fmt.Errorf("invalid UTF-16 mailbox")
					}
					i++
				} else if units[i] >= 0xdc00 && units[i] <= 0xdfff {
					return "", fmt.Errorf("invalid UTF-16 mailbox")
				}
			}
			out.WriteString(string(utf16.Decode(units)))
		}
		s = s[end+1:]
	}
	if !utf8.ValidString(out.String()) {
		return "", fmt.Errorf("invalid mailbox encoding")
	}
	return out.String(), nil
}
