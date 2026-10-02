package imapclient

import (
	"bufio"
	"fmt"
	"net"
	"strings"
	"testing"
)

func folderTestClient(t *testing.T, caps map[string]bool, exchanges [][2]string) *Client {
	t.Helper()
	client, server := net.Pipe()
	c := &Client{conn: client, caps: caps}
	c.resetIO()
	done := make(chan error, 1)
	go func() {
		r := bufio.NewReader(server)
		for _, e := range exchanges {
			line, err := r.ReadString('\n')
			if err != nil {
				done <- err
				return
			}
			tag, command, _ := strings.Cut(strings.TrimSuffix(line, "\r\n"), " ")
			if command != e[0] {
				done <- fmt.Errorf("got %q want %q", command, e[0])
				server.Close()
				return
			}
			if _, err = fmt.Fprintf(server, "%s%s OK complete\r\n", e[1], tag); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	t.Cleanup(func() {
		client.Close()
		server.Close()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	return c
}
func TestListAndCreateIMAPFolders(t *testing.T) {
	c := folderTestClient(t, nil, [][2]string{
		{`LIST "" "*"`, "* LIST () \".\" \"INBOX\"\r\n* LIST (\\NoSelect) \".\" \"Groups\"\r\n* LIST (\\NoInferiors) \".\" {5}\r\nStuff\r\n* LIST () \".\" \"&ZcWITA-\"\r\n"},
		{`LSUB "" "*"`, "* LSUB () \".\" \"INBOX\"\r\n* LSUB () \".\" \"Deleted\"\r\n"},
		{`CREATE "&ZcWITA-.&- Work"`, ""},
		{`SUBSCRIBE "&ZcWITA-.&- Work"`, ""},
		{`UNSUBSCRIBE "&ZcWITA-.&- Work"`, ""},
		{`SELECT "&ZcWITA-"`, "* OK [UIDVALIDITY 42] valid\r\n"},
	})
	rows, err := c.ListMailboxes()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 4 || !rows[0].Subscribed || rows[1].Selectable || rows[2].Name != "Stuff" || rows[2].CanCreateChild || rows[3].Name != "旅行" {
		t.Fatal(rows)
	}
	for _, fn := range []func() error{func() error { return c.CreateMailbox("旅行.& Work") }, func() error { return c.SetSubscribed("旅行.& Work", true) }, func() error { return c.SetSubscribed("旅行.& Work", false) }, func() error { _, err := c.Select("旅行"); return err }} {
		if err := fn(); err != nil {
			t.Fatal(err)
		}
	}
}
func TestRev2FoldersUseUTF8AndSubscribedList(t *testing.T) {
	c := folderTestClient(t, map[string]bool{"IMAP4REV2": true}, [][2]string{
		{`LIST "" "*" RETURN (SUBSCRIBED)`, "* LIST (\\Subscribed) \"/\" \"旅行\"\r\n"},
		{`CREATE "Café"`, ""},
	})
	rows, err := c.ListMailboxes()
	if err != nil || len(rows) != 1 || !rows[0].Subscribed || rows[0].Name != "旅行" {
		t.Fatal(rows, err)
	}
	if err := c.CreateMailbox("Café"); err != nil {
		t.Fatal(err)
	}
}
func TestUTF7RoundTripAndRejectMalformed(t *testing.T) {
	for _, s := range []string{"INBOX", "A & B", "旅行/仕事", "Café 😃"} {
		encoded := encodeUTF7(s)
		got, err := decodeUTF7(encoded)
		if err != nil || got != s {
			t.Fatal(s, encoded, got, err)
		}
	}
	for _, s := range []string{"&bad", "&!-", "&2AA-"} {
		if _, err := decodeUTF7(s); err == nil {
			t.Fatal("accepted malformed UTF7", s)
		}
	}
	c := &Client{}
	for _, s := range []string{"", "x\r\nCREATE z", "x\x00y"} {
		if err := c.CreateMailbox(s); err == nil {
			t.Fatal("accepted invalid name")
		}
	}
}
