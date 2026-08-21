package imapclient

import (
	"bufio"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	literalRE     = regexp.MustCompile(`\{([0-9]+)\}$`)
	uidValidityRE = regexp.MustCompile(`(?i)\[UIDVALIDITY ([0-9]+)\]`)
	flagsRE       = regexp.MustCompile(`(?i)FLAGS \(([^)]*)\)`)
)

const commandTimeout = 5 * time.Minute

type Client struct {
	conn       net.Conn
	r          *bufio.Reader
	w          *bufio.Writer
	tag        uint64
	caps       map[string]bool
	preauth    bool
	selected   string
	serverName string
}

type SelectInfo struct {
	UIDValidity uint32
	Messages    uint32
}

type Message struct {
	UID  uint32
	Seen bool
	Raw  []byte
}

func Dial(address, security string, timeout time.Duration) (*Client, error) {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("imap address must include host:port: %w", err)
	}
	var conn net.Conn
	d := &net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}
	if security == "tls" {
		conn, err = tls.DialWithDialer(d, "tcp", address, &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
	} else {
		conn, err = d.Dial("tcp", address)
	}
	if err != nil {
		return nil, err
	}
	c := &Client{conn: conn, serverName: host}
	c.resetIO()
	line, err := c.readLine()
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("imap greeting: %w", err)
	}
	upper := strings.ToUpper(line)
	if !strings.HasPrefix(upper, "* OK") && !strings.HasPrefix(upper, "* PREAUTH") {
		_ = conn.Close()
		return nil, fmt.Errorf("imap rejected connection: %s", line)
	}
	c.preauth = strings.HasPrefix(upper, "* PREAUTH")
	if security == "starttls" {
		if _, err := c.simple("STARTTLS", nil); err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("imap STARTTLS: %w", err)
		}
		tlsConn := tls.Client(c.conn, &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
		if err := tlsConn.Handshake(); err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("imap TLS handshake: %w", err)
		}
		c.conn = tlsConn
		c.resetIO()
	}
	return c, nil
}

func (c *Client) resetIO() {
	c.r = bufio.NewReaderSize(c.conn, 64*1024)
	c.w = bufio.NewWriterSize(c.conn, 64*1024)
}

func (c *Client) Close() error {
	if c.conn == nil {
		return nil
	}
	_, _ = c.simple("LOGOUT", nil)
	return c.conn.Close()
}

// Abort closes the transport immediately. It is used to interrupt a blocked
// network operation when the caller cancels the sync context.
func (c *Client) Abort() error {
	if c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

func (c *Client) Login(username, password string) error {
	if c.preauth {
		return c.loadCapabilities()
	}
	if _, err := c.simple("LOGIN "+quote(username)+" "+quote(password), nil); err != nil {
		return err
	}
	return c.loadCapabilities()
}

func (c *Client) loadCapabilities() error {
	c.caps = map[string]bool{}
	_, err := c.simple("CAPABILITY", func(line string) error {
		if strings.HasPrefix(strings.ToUpper(line), "* CAPABILITY ") {
			for _, cap := range strings.Fields(line)[2:] {
				c.caps[strings.ToUpper(cap)] = true
			}
		}
		return nil
	})
	return err
}

func (c *Client) HasCapability(name string) bool {
	return c.caps[strings.ToUpper(name)]
}

func (c *Client) Select(mailbox string) (SelectInfo, error) {
	var info SelectInfo
	_, err := c.simple("SELECT "+quote(mailbox), func(line string) error {
		if m := uidValidityRE.FindStringSubmatch(line); len(m) == 2 {
			n, _ := strconv.ParseUint(m[1], 10, 32)
			info.UIDValidity = uint32(n)
		}
		fields := strings.Fields(line)
		if len(fields) >= 3 && fields[0] == "*" && strings.EqualFold(fields[2], "EXISTS") {
			n, _ := strconv.ParseUint(fields[1], 10, 32)
			info.Messages = uint32(n)
		}
		return nil
	})
	if err == nil {
		c.selected = mailbox
	}
	return info, err
}

func (c *Client) UIDSearchAll() ([]uint32, error) {
	var ids []uint32
	_, err := c.simple("UID SEARCH ALL", func(line string) error {
		if !strings.HasPrefix(strings.ToUpper(line), "* SEARCH") {
			return nil
		}
		for _, f := range strings.Fields(line)[2:] {
			n, err := strconv.ParseUint(f, 10, 32)
			if err != nil {
				return fmt.Errorf("bad UID in SEARCH response %q", f)
			}
			ids = append(ids, uint32(n))
		}
		return nil
	})
	return ids, err
}

func (c *Client) UIDFetch(uid uint32) (Message, error) {
	var out Message
	out.UID = uid
	if err := c.setCommandDeadline(); err != nil {
		return out, err
	}
	tag := c.nextTag()
	cmd := fmt.Sprintf("%s UID FETCH %d (UID FLAGS BODY.PEEK[])\r\n", tag, uid)
	if err := c.write(cmd); err != nil {
		return out, err
	}
	for {
		line, err := c.readLine()
		if err != nil {
			return out, err
		}
		if strings.HasPrefix(line, tag+" ") {
			if !taggedOK(line) {
				return out, fmt.Errorf("imap fetch failed: %s", line)
			}
			if out.Raw == nil {
				return out, errors.New("imap FETCH returned no message literal")
			}
			return out, nil
		}
		if m := flagsRE.FindStringSubmatch(line); len(m) == 2 {
			for _, f := range strings.Fields(m[1]) {
				if strings.EqualFold(f, `\Seen`) {
					out.Seen = true
				}
			}
		}
		m := literalRE.FindStringSubmatch(line)
		if len(m) != 2 {
			continue
		}
		n, err := strconv.ParseInt(m[1], 10, 64)
		if err != nil || n < 0 {
			return out, fmt.Errorf("bad IMAP literal length in %q", line)
		}
		out.Raw = make([]byte, n)
		if _, err := io.ReadFull(c.r, out.Raw); err != nil {
			return out, fmt.Errorf("read IMAP literal: %w", err)
		}
	}
}

func (c *Client) DeleteUID(uid uint32, allowUnsafeExpunge bool) error {
	if c.selected == "" {
		return errors.New("no IMAP mailbox selected")
	}
	if !c.HasCapability("UIDPLUS") && !allowUnsafeExpunge {
		return errors.New("server lacks UIDPLUS; refusing EXPUNGE because it could remove other messages already marked \\Deleted (set allow_expunge_without_uidplus to override)")
	}
	if _, err := c.simple(fmt.Sprintf(`UID STORE %d +FLAGS.SILENT (\Deleted)`, uid), nil); err != nil {
		return err
	}
	if c.HasCapability("UIDPLUS") {
		_, err := c.simple(fmt.Sprintf("UID EXPUNGE %d", uid), nil)
		return err
	}
	_, err := c.simple("EXPUNGE", nil)
	return err
}

func (c *Client) simple(command string, onLine func(string) error) (string, error) {
	if err := c.setCommandDeadline(); err != nil {
		return "", err
	}
	tag := c.nextTag()
	if err := c.write(tag + " " + command + "\r\n"); err != nil {
		return "", err
	}
	for {
		line, err := c.readLine()
		if err != nil {
			return "", err
		}
		if strings.HasPrefix(line, tag+" ") {
			if !taggedOK(line) {
				return line, fmt.Errorf("imap command failed: %s", line)
			}
			return line, nil
		}
		if onLine != nil {
			if err := onLine(line); err != nil {
				return "", err
			}
		}
	}
}

func (c *Client) setCommandDeadline() error {
	if c.conn == nil {
		return errors.New("IMAP connection is closed")
	}
	return c.conn.SetDeadline(time.Now().Add(commandTimeout))
}

func (c *Client) nextTag() string {
	c.tag++
	return fmt.Sprintf("A%04d", c.tag)
}

func (c *Client) write(s string) error {
	if _, err := c.w.WriteString(s); err != nil {
		return err
	}
	return c.w.Flush()
}

func (c *Client) readLine() (string, error) {
	line, err := c.r.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r"), nil
}

func taggedOK(line string) bool {
	fields := strings.Fields(line)
	return len(fields) >= 2 && strings.EqualFold(fields[1], "OK")
}

func quote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}
