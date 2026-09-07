package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"git.cerberusgames.ca/Starstreak/MailSalonSync/internal/config"
	"git.cerberusgames.ca/Starstreak/MailSalonSync/internal/status"
	"git.cerberusgames.ca/Starstreak/MailSalonSync/internal/syncer"
)

const version = "0.5.4"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "MailSalonSync:", err)
		os.Exit(1)
	}
}

func run() error {
	root := flag.NewFlagSet("MailSalonSync", flag.ContinueOnError)
	root.SetOutput(os.Stderr)
	configPath := root.String("config", defaultConfigPath(), "path to TOML config")
	plain := root.Bool("plain", false, "disable interactive status UI and run synchronously")
	root.Usage = func() { usage(root) }
	if err := root.Parse(os.Args[1:]); err != nil {
		return err
	}
	args := root.Args()
	if len(args) == 0 {
		usage(root)
		return nil
	}

	switch args[0] {
	case "version", "--version", "-version":
		fmt.Println(version)
		return nil
	case "example-config":
		fmt.Print(exampleConfig)
		return nil
	case "sync":
		return runSync(*configPath, args[1:], *plain)
	case "adopt-existing":
		return runAdoptExisting(*configPath, args[1:], *plain)
	case "upload-existing":
		return runUploadExisting(*configPath, args[1:], *plain)
	case "jmap-send":
		return runJMAPSend(*configPath, args[1:], *plain)
	case "check-config":
		_, err := config.Load(*configPath)
		if err != nil {
			return err
		}
		fmt.Println("configuration is valid")
		return nil
	case "help", "-h", "--help":
		usage(root)
		return nil
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func parseAccountNames(raw string) []string {
	var names []string
	for _, n := range strings.Split(raw, ",") {
		if n = strings.TrimSpace(n); n != "" {
			names = append(names, n)
		}
	}
	return names
}

func runSync(path string, args []string, plain bool) error {
	fs := flag.NewFlagSet("sync", flag.ContinueOnError)
	accounts := fs.String("account", "", "account name or comma-separated account names; default is all")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	names := parseAccountNames(*accounts)
	task := func(r status.Reporter) error {
		return syncer.Sync(ctx, cfg, names, r)
	}
	if plain {
		return status.RunPlain(task)
	}
	return status.Run(cancel, task)
}

func runAdoptExisting(path string, args []string, plain bool) error {
	fs := flag.NewFlagSet("adopt-existing", flag.ContinueOnError)
	accounts := fs.String("account", "", "account name or comma-separated account names; default is all")
	dryRun := fs.Bool("dry-run", false, "report matches without renaming local files or changing state")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	names := parseAccountNames(*accounts)
	var summary syncer.AdoptSummary
	task := func(r status.Reporter) error {
		s, err := syncer.AdoptExisting(ctx, cfg, names, syncer.AdoptOptions{DryRun: *dryRun}, r)
		summary = s
		return err
	}
	if plain {
		err = status.RunPlain(task)
	} else {
		err = status.Run(cancel, task)
	}
	if err != nil {
		return err
	}
	if *dryRun {
		fmt.Printf("dry run: would adopt %d message(s); would retire %d legacy duplicate(s) to adopt-backup; skipped %d\n", summary.Adopted, summary.Retired, summary.Skipped)
	} else {
		fmt.Printf("adopted %d message(s); retired %d legacy duplicate(s) to adopt-backup; skipped %d\n", summary.Adopted, summary.Retired, summary.Skipped)
	}
	return nil
}

func printUploadSummary(prefix string, summary syncer.UploadExistingSummary) {
	fmt.Printf("%s uploaded=%d; adopted=%d; elsewhere=%d; ambiguous=%d; duplicate-local-to-backup=%d (exact=%d message-id=%d header=%d)\n",
		prefix,
		summary.Uploaded,
		summary.Adopted,
		summary.Elsewhere,
		summary.Ambiguous,
		summary.Duplicates,
		summary.DuplicateExact,
		summary.DuplicateMessageID,
		summary.DuplicateHeader,
	)
}

func runUploadExisting(path string, args []string, plain bool) error {
	fs := flag.NewFlagSet("upload-existing", flag.ContinueOnError)
	accounts := fs.String("account", "", "account name or comma-separated account names; default is all JMAP accounts")
	dryRun := fs.Bool("dry-run", false, "report local-only messages without importing or renaming them")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	names := parseAccountNames(*accounts)
	var summary syncer.UploadExistingSummary
	task := func(r status.Reporter) error {
		s, err := syncer.UploadExisting(ctx, cfg, names, syncer.UploadExistingOptions{DryRun: *dryRun}, r)
		summary = s
		return err
	}
	if plain {
		err = status.RunPlain(task)
	} else {
		err = status.Run(cancel, task)
	}
	if err != nil {
		if errors.Is(err, syncer.ErrUploadQuotaExceeded) {
			printUploadSummary("upload-existing paused safely:", summary)
		}
		return err
	}
	if *dryRun {
		fmt.Printf("dry run: would upload %d message(s); would adopt %d existing remote match(es); elsewhere=%d; ambiguous=%d; duplicate-local-to-backup=%d (exact=%d message-id=%d header=%d)\n", summary.Uploaded, summary.Adopted, summary.Elsewhere, summary.Ambiguous, summary.Duplicates, summary.DuplicateExact, summary.DuplicateMessageID, summary.DuplicateHeader)
	} else {
		fmt.Printf("uploaded %d message(s); adopted %d existing remote match(es); elsewhere=%d; ambiguous=%d; retired duplicate-local-to-backup=%d (exact=%d message-id=%d header=%d)\n", summary.Uploaded, summary.Adopted, summary.Elsewhere, summary.Ambiguous, summary.Duplicates, summary.DuplicateExact, summary.DuplicateMessageID, summary.DuplicateHeader)
	}
	return nil
}

func runJMAPSend(path string, args []string, plain bool) error {
	fs := flag.NewFlagSet("jmap-send", flag.ContinueOnError)
	accountName := fs.String("account", "", "JMAP account name (required)")
	file := fs.String("file", "", "RFC 5322 message file; stdin if omitted or '-' ")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *accountName == "" {
		return fmt.Errorf("jmap-send requires -account")
	}
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}
	a, err := cfg.Account(*accountName)
	if err != nil {
		return err
	}
	var raw []byte
	if *file == "" || *file == "-" {
		raw, err = io.ReadAll(os.Stdin)
	} else {
		raw, err = os.ReadFile(config.ExpandPath(*file))
	}
	if err != nil {
		return err
	}
	if len(raw) == 0 {
		return fmt.Errorf("message is empty")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	var sentID string
	task := func(r status.Reporter) error {
		id, err := syncer.SendJMAP(ctx, a, raw, r)
		sentID = id
		return err
	}
	if plain {
		err = status.RunPlain(task)
	} else {
		err = status.Run(cancel, task)
	}
	if err != nil {
		if sentID != "" {
			return fmt.Errorf("%w (imported draft email id: %s)", err, sentID)
		}
		return err
	}
	fmt.Println("sent", sentID)
	return nil
}

func defaultConfigPath() string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "MailSalonSync", "config.toml")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "config.toml"
	}
	return filepath.Join(home, ".config", "MailSalonSync", "config.toml")
}

func usage(fs *flag.FlagSet) {
	out := fs.Output()
	fmt.Fprintln(out, "MailSalonSync - Maildir synchronizer for IMAP and JMAP")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Usage:")
	fmt.Fprintln(out, "  MailSalonSync [-config PATH] [-plain] sync [-account NAME[,NAME...]]")
	fmt.Fprintln(out, "  MailSalonSync [-config PATH] [-plain] adopt-existing [-account NAME[,NAME...]] [-dry-run]")
	fmt.Fprintln(out, "  MailSalonSync [-config PATH] [-plain] upload-existing [-account NAME[,NAME...]] [-dry-run]")
	fmt.Fprintln(out, "  MailSalonSync [-config PATH] [-plain] jmap-send -account NAME [-file MESSAGE.eml]")
	fmt.Fprintln(out, "  MailSalonSync [-config PATH] check-config")
	fmt.Fprintln(out, "  MailSalonSync example-config")
	fmt.Fprintln(out, "  MailSalonSync version")
	fmt.Fprintln(out)
	fs.PrintDefaults()
}

const exampleConfig = `state_dir = "~/.local/state/MailSalonSync"

[[accounts]]
name = "personal-imap"
protocol = "imap"
local_root = "~/Mail/personal"
propagate_deletes = true

[[accounts.mailboxes]]
remote = "INBOX"
local = "INBOX"

[[accounts.mailboxes]]
remote = "Archive"
local = "Archive"

[accounts.imap]
address = "imap.example.com:993"
username = "me@example.com"
password_env = "PERSONAL_IMAP_PASSWORD"
security = "tls"

[[accounts]]
name = "work-jmap"
protocol = "jmap"
local_root = "~/Mail/work"
propagate_deletes = true

[[accounts.mailboxes]]
remote = "role:inbox"
local = "INBOX"

[[accounts.mailboxes]]
remote = "role:sent"
local = "Sent"

[[accounts.mailboxes]]
remote = "Archive"
local = "Archive"

[[accounts.mailboxes]]
remote = "Archive/Projects"
local = "Projects"

[accounts.jmap]
session_url = "https://mail.example.net/.well-known/jmap"
auth = "basic"
username = "me@example.net"
password_command = "pass show mail/example.net"
drafts_mailbox = "role:drafts"
sent_mailbox = "role:sent"
`
