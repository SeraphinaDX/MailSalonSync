package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"

	"git.cerberusgames.ca/Starstreak/MailSalonSync/internal/config"
	"golang.org/x/term"
)

func passwordTarget(cfg *config.Config, account, collection string) (config.Credential, bool, error) {
	if (account == "") == (collection == "") {
		return config.Credential{}, false, fmt.Errorf("password requires exactly one of -account or -collection")
	}
	if account != "" {
		a, err := cfg.Account(account)
		if err != nil {
			return config.Credential{}, false, err
		}
		return a.Credential(), a.ExternalSecret(), nil
	}
	for i := range cfg.Collections {
		c := &cfg.Collections[i]
		if c.Name == collection {
			return c.Credential(), c.ExternalSecret(), nil
		}
	}
	return config.Credential{}, false, fmt.Errorf("no collection named %q", collection)
}

func runPassword(path string, args []string) error {
	fs := flag.NewFlagSet("password", flag.ContinueOnError)
	account := fs.String("account", "", "configured account name")
	collection := fs.String("collection", "", "configured collection name")
	remove := fs.Bool("delete", false, "delete the stored credential")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("password does not accept positional arguments or passwords on the command line")
	}
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}
	var targets []config.Credential
	if *account != "" || *collection != "" {
		target, external, err := passwordTarget(cfg, *account, *collection)
		if err != nil {
			return err
		}
		if *remove {
			return target.Delete()
		}
		if external {
			return fmt.Errorf("%q uses a configured environment/command secret source", target.Name)
		}
		targets = append(targets, target)
	} else {
		if *remove {
			return fmt.Errorf("use -account or -collection with -delete")
		}
		targets = passwordGroups(cfg)
		if len(targets) == 0 {
			fmt.Fprintln(os.Stdout, "All accounts and collections use environment variables or password commands.")
			return nil
		}
	}
	for _, target := range targets {
		fmt.Fprintf(os.Stderr, "Password for %s (%s at %s)\n", target.Name, target.Username, target.Endpoint)
		if err := storePassword(target); err != nil {
			return err
		}
	}
	return nil
}

// passwordGroups asks once for each shared login, listing all its users.
func passwordGroups(cfg *config.Config) []config.Credential {
	var targets []config.Credential
	add := func(c config.Credential, external bool) {
		if external {
			return
		}
		for i := range targets {
			if targets[i].SameLogin(c) {
				targets[i].Name += ", " + c.Name
				return
			}
		}
		targets = append(targets, c)
	}
	for i := range cfg.Accounts {
		a := &cfg.Accounts[i]
		add(a.Credential(), a.ExternalSecret())
	}
	for i := range cfg.Collections {
		c := &cfg.Collections[i]
		add(c.Credential(), c.ExternalSecret())
	}
	return targets
}

func storePassword(target config.Credential) error {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return fmt.Errorf("password requires an interactive terminal")
	}
	read := func(prompt string) ([]byte, error) {
		fmt.Fprint(os.Stderr, prompt)
		secret, err := term.ReadPassword(fd)
		fmt.Fprintln(os.Stderr)
		return secret, err
	}
	secret, err := read("Password or token: ")
	if err != nil {
		return err
	}
	defer clear(secret)
	confirm, err := read("Confirm password or token: ")
	if err != nil {
		return err
	}
	defer clear(confirm)
	if !bytes.Equal(secret, confirm) {
		return fmt.Errorf("passwords do not match")
	}
	if err := target.Store(string(secret)); err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "Credential stored in OS keyring for %q.\n", target.Name)
	return nil
}
