package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"git.cerberusgames.ca/Starstreak/MailSalonSync/internal/config"
	"git.cerberusgames.ca/Starstreak/MailSalonSync/internal/folders"
)

// Reload under the lock: another command may have updated managed mappings
// between the initial config read and lock acquisition.
func loadMailOperation(path string) (*config.Config, func(), error) {
	cfg, err := config.Load(path)
	if err != nil {
		return nil, nil, err
	}
	unlock, err := cfg.LockOperations()
	if err != nil {
		return nil, nil, err
	}
	cfg, err = config.Load(path)
	if err != nil {
		unlock()
		return nil, nil, err
	}
	return cfg, unlock, nil
}
func runFolders(path string, args []string) error {
	fs := flag.NewFlagSet("folders", flag.ContinueOnError)
	account := fs.String("account", "", "configured sync account (required)")
	action := fs.String("action", "list", "list, create, subscribe or unsubscribe")
	mailbox := fs.String("mailbox", "", "mailbox ID from folders list")
	name := fs.String("name", "", "single new folder name")
	parent := fs.String("parent", "", "parent mailbox ID from folders list")
	local := fs.String("local", "", "relative local folder; default Folders/<name>")
	stdin := fs.Bool("request-stdin", false, "read one JSON request from stdin (for GUI callers)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *account == "" || len(fs.Args()) != 0 {
		return fmt.Errorf("folders requires -account NAME and no positional arguments")
	}
	req := folders.Request{Action: *action, Mailbox: *mailbox, Name: *name, Parent: *parent, Local: *local}
	if *stdin {
		dec := json.NewDecoder(io.LimitReader(os.Stdin, 1024*1024))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil {
			return fmt.Errorf("folder request: %w", err)
		}
		var extra any
		if err := dec.Decode(&extra); err != io.EOF {
			return fmt.Errorf("expected one folder request")
		}
	}
	cfg, unlock, err := loadMailOperation(path)
	if err != nil {
		return err
	}
	defer unlock()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	response, err := folders.Run(ctx, cfg, *account, req)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(response)
}
