package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"git.cerberusgames.ca/Starstreak/MailSalonSync/internal/folders"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestFolderCommandRejectsWrongRootBeforeConnecting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	text := fmt.Sprintf("state_dir = %q\n[[accounts]]\nname = 'mail'\nprotocol = 'imap'\nlocal_root = %q\n[[accounts.mailboxes]]\nremote = 'INBOX'\nlocal = '.'\n[accounts.imap]\naddress = '127.0.0.1:1'\nusername = 'me'\npassword_env = 'MISSING_SECRET'\n", t.TempDir(), t.TempDir())
	os.WriteFile(path, []byte(text), 0600)
	input := filepath.Join(t.TempDir(), "request.json")
	os.WriteFile(input, []byte(`{"action":"create","name":"Work","expected_local_root":"/wrong-root"}`), 0600)
	f, err := os.Open(input)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	old := os.Stdin
	os.Stdin = f
	defer func() { os.Stdin = old }()
	if err := runFolders(path, []string{"-account", "mail", "-request-stdin"}); err == nil || !strings.Contains(err.Error(), "not the GUI Maildir") {
		t.Fatal(err)
	}
	cfg, unlock, err := loadMailOperation(path)
	if err != nil {
		t.Fatal("failed command leaked lock", err)
	}
	_ = cfg
	unlock()
}

// Exercise the real CLI/config/protocol path against a disposable IMAP server,
// then verify normal sync selects the saved folder and skips it after unsubscribe.
func TestFolderCommandPersistsMappingUsedBySync(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	var mu sync.Mutex
	boxes := map[string]bool{"INBOX": true}
	selected := make(chan string, 16)
	acceptDone := make(chan struct{})
	go func() {
		defer close(acceptDone)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(10 * time.Second))
				fmt.Fprint(conn, "* OK test server\r\n")
				reader := bufio.NewReader(conn)
				for {
					line, err := reader.ReadString('\n')
					if err != nil {
						return
					}
					tag, command, _ := strings.Cut(strings.TrimSuffix(line, "\r\n"), " ")
					ok := true
					mu.Lock()
					switch {
					case strings.HasPrefix(command, "LOGIN "):
					case command == "CAPABILITY":
						fmt.Fprint(conn, "* CAPABILITY IMAP4rev1 UIDPLUS MOVE\r\n")
					case command == `LIST "" "*"`:
						for name := range boxes {
							fmt.Fprintf(conn, "* LIST () \".\" %q\r\n", name)
						}
					case command == `LSUB "" "*"`:
						for name, on := range boxes {
							if on {
								fmt.Fprintf(conn, "* LSUB () \".\" %q\r\n", name)
							}
						}
					case strings.HasPrefix(command, "CREATE "):
						name := strings.TrimPrefix(command, "CREATE ")
						name = strings.Trim(name, "\"")
						if _, exists := boxes[name]; exists {
							ok = false
						} else {
							boxes[name] = false
						}
					case strings.HasPrefix(command, "SUBSCRIBE "):
						name := strings.Trim(strings.TrimPrefix(command, "SUBSCRIBE "), "\"")
						boxes[name] = true
					case strings.HasPrefix(command, "UNSUBSCRIBE "):
						name := strings.Trim(strings.TrimPrefix(command, "UNSUBSCRIBE "), "\"")
						boxes[name] = false
					case strings.HasPrefix(command, "SELECT "):
						name := strings.Trim(strings.TrimPrefix(command, "SELECT "), "\"")
						if _, exists := boxes[name]; !exists {
							ok = false
						} else {
							selected <- name
							fmt.Fprint(conn, "* OK [UIDVALIDITY 7] valid\r\n* 0 EXISTS\r\n")
						}
					case command == "UID SEARCH ALL":
						fmt.Fprint(conn, "* SEARCH\r\n")
					case command == "LOGOUT":
						fmt.Fprint(conn, "* BYE done\r\n")
					default:
						ok = false
						t.Errorf("unexpected protocol mutation %q", command)
					}
					mu.Unlock()
					status := "OK"
					if !ok {
						status = "NO"
					}
					fmt.Fprintf(conn, "%s %s done\r\n", tag, status)
					if command == "LOGOUT" {
						return
					}
				}
			}()
		}
	}()
	t.Cleanup(func() { listener.Close(); <-acceptDone; workers.Wait() })
	root, state := t.TempDir(), t.TempDir()
	path := filepath.Join(t.TempDir(), "config.toml")
	text := fmt.Sprintf("# keep main config intact\nstate_dir = %q\n[[accounts]]\nname = 'mail'\nprotocol = 'imap'\nlocal_root = %q\npropagate_deletes = true\n[[accounts.mailboxes]]\nremote = 'INBOX'\nlocal = 'INBOX'\n[accounts.imap]\naddress = %q\nsecurity = 'plain'\nusername = 'fixture'\npassword = 'fixture-only'\n", state, root, listener.Addr().String())
	os.WriteFile(path, []byte(text), 0600)
	capture := func(fn func() error) []byte {
		t.Helper()
		f, err := os.CreateTemp(t.TempDir(), "output")
		if err != nil {
			t.Fatal(err)
		}
		old := os.Stdout
		os.Stdout = f
		err = fn()
		os.Stdout = old
		f.Close()
		if err != nil {
			t.Fatal(err)
		}
		data, _ := os.ReadFile(f.Name())
		return data
	}
	data := capture(func() error {
		return runFolders(path, []string{"-account", "mail", "-action", "create", "-name", "Work", "-parent", "INBOX"})
	})
	var response folders.Response
	if err := json.Unmarshal(data, &response); err != nil {
		t.Fatal(string(data), err)
	}
	var added folders.Folder
	for _, f := range response.Folders {
		if f.ID == "INBOX.Work" {
			added = f
		}
	}
	if !added.Syncing || !added.Subscribed || added.Local != "Folders/INBOX/Work" {
		t.Fatal(response)
	}
	capture(func() error { return runSync(path, []string{"-account", "mail"}, true) })
	seen := map[string]bool{}
	for len(selected) > 0 {
		seen[<-selected] = true
	}
	if !seen["INBOX.Work"] || !seen["INBOX"] {
		t.Fatal("normal sync ignored managed mapping", seen)
	}
	cached := filepath.Join(root, added.Local, "cur", "cached:2,S")
	os.WriteFile(cached, []byte("cached mail"), 0600)
	capture(func() error {
		return runFolders(path, []string{"-account", "mail", "-action", "unsubscribe", "-mailbox", "INBOX.Work"})
	})
	capture(func() error { return runSync(path, []string{"-account", "mail"}, true) })
	for len(selected) > 0 {
		if name := <-selected; name != "INBOX" {
			t.Fatal("sync selected unsubscribed folder", name)
		}
	}
	if bytes, err := os.ReadFile(cached); err != nil || string(bytes) != "cached mail" {
		t.Fatal("unsubscribe lost mail", err)
	}
	capture(func() error {
		return runFolders(path, []string{"-account", "mail", "-action", "unsubscribe", "-mailbox", "INBOX"})
	})
	listener.Close()
	// All mappings disabled: sync should succeed without contacting the server.
	capture(func() error { return runSync(path, []string{"-account", "mail"}, true) })
	if bytes, _ := os.ReadFile(path); string(bytes) != text {
		t.Fatal("main config was rewritten")
	}
}
