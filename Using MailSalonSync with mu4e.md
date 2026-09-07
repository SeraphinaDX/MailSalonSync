## Using MailSalonSync with mu4e

MailSalonSync can be used as both the synchronization backend and outgoing mail transport for **mu4e** in Emacs. This allows a mu4e setup to use MailSalonSync instead of tools such as OfflineIMAP and msmtp.

The basic flow is:

```text
mu4e
  |
  +-- receive/sync --> MailSalonSync -plain sync --> IMAP/JMAP --> Maildir
  |
  +-- send ---------> MailSalonSync -plain jmap-send --> JMAP
```

### Maildir setup

MailSalonSync, `mu`, and mu4e must all use the same Maildir.

For example:

```text
~/Maildir
```

The corresponding MailSalonSync account should have its local mail directory configured as `~/Maildir` in `config.toml`.

If `mu` has not yet been initialized for this Maildir, initialize and index it:

```sh
mu init --maildir=~/Maildir --personal-address=user@example.com
mu index
```

You can verify the Maildir known to `mu` with:

```sh
mu info
```

### Example Emacs configuration

The following example assumes:

- MailSalonSync is installed at `~/bin/MailSalonSync`
- MailSalonSync's configuration is stored at `~/.config/MailSalonSync/config.toml`
- The MailSalonSync account is named `example-jmap`
- Local mail is stored in `~/Maildir`
- The email address is `user@example.com`

```elisp
;; --- Mu4e Core Setup ---
(require 'mu4e)

;; Local mail directory used by MailSalonSync and mu.
(setq mu4e-maildir (expand-file-name "~/Maildir"))

;; --- MailSalonSync Setup ---

(setq my-mailsalonsync-program
      (expand-file-name "~/bin/MailSalonSync"))

(setq my-mailsalonsync-config
      (expand-file-name "~/.config/MailSalonSync/config.toml"))

(setq my-mailsalonsync-account "example-jmap")

;; mu4e invokes its retrieval command through a PTY. Force MailSalonSync's
;; synchronous line-oriented mode so the process returns only after sync is
;; complete and mu can safely begin indexing.
(setq mu4e-get-mail-command
      (mapconcat
       #'shell-quote-argument
       (list my-mailsalonsync-program
             (concat "-config=" my-mailsalonsync-config)
             "-plain"
             "sync")
       " "))

(setq mu4e-update-interval 300)

;; --- Folder Mappings ---
(setq mu4e-drafts-folder "/Drafts"
      mu4e-sent-folder   "/Sent"
      mu4e-trash-folder  "/Trash"
      mu4e-refile-folder "/Archive")

;; --- User Identity ---
(setq user-mail-address "user@example.com"
      user-full-name "Example User"
      mu4e-user-mail-address-list '("user@example.com"))

;; --- Sending Mail Through MailSalonSync/JMAP ---
(defun my-mailsalonsync-send-mail ()
  "Send the current message using MailSalonSync JMAP."
  (let ((output-buffer
         (get-buffer-create "*MailSalonSync Send*")))
    (with-current-buffer output-buffer
      (erase-buffer))

    (let ((status
           (call-process-region
            (point-min)
            (point-max)
            my-mailsalonsync-program
            nil
            output-buffer
            nil
            (concat "-config=" my-mailsalonsync-config)
            "-plain"
            "jmap-send"
            "-account"
            my-mailsalonsync-account)))

      (unless (and (integerp status)
                   (zerop status))
        (error
         "MailSalonSync send failed: %s"
         (with-current-buffer output-buffer
           (string-trim (buffer-string))))))))

(setq send-mail-function
      #'my-mailsalonsync-send-mail
      message-send-mail-function
      #'my-mailsalonsync-send-mail)

;; --- Optional Mu4e Settings ---
(setq mu4e-change-filenames-when-moving t)

(setq mu4e-view-show-addresses t
      mu4e-view-show-images t)

(setq mu4e-attachment-dir
      (expand-file-name "~/Downloads/Emacs"))

(global-set-key (kbd "C-c m") #'mu4e)
```

### MailSalonSync account name

The value of:

```elisp
(setq my-mailsalonsync-account "example-jmap")
```

must match the `name` of the corresponding account in the MailSalonSync TOML configuration.

For example:

```toml
[[accounts]]
name = "personal-jmap"
protocol = "jmap"
local_root = "~/Maildir"
```

then the Emacs configuration should contain:

```elisp
(setq my-mailsalonsync-account "personal-jmap")
```

### Synchronizing mail

From mu4e, running:

```text
M-x mu4e-update-mail-and-index
```

causes mu4e to execute:

```sh
~/bin/MailSalonSync -config=~/.config/MailSalonSync/config.toml -plain sync
```

`-plain` is important for mu4e. mu4e allocates a pseudo-terminal for the retrieval process; without the explicit flag, a terminal-aware program may mistakenly start an interactive UI. MailSalonSync's plain mode is synchronous, so it returns success only after the sync operation has completed. mu4e can then safely run `mu index` against the updated Maildir.

### Sending mail

Outgoing messages do not require `msmtp` or Emacs SMTP configuration.

The custom `my-mailsalonsync-send-mail` function passes the completed email message to:

```sh
MailSalonSync -plain jmap-send -account example-jmap
```

MailSalonSync then submits the message through the configured JMAP account.

This means an existing configuration using:

```elisp
(setq sendmail-program "/usr/bin/msmtp")
```

or Emacs SMTP settings such as:

```elisp
smtpmail-smtp-server
smtpmail-smtp-service
smtpmail-auth-credentials
```

can be removed when MailSalonSync is handling outgoing JMAP mail.

### Troubleshooting

Validate the TOML first:

```sh
MailSalonSync -config=~/.config/MailSalonSync/config.toml check-config
```

If mu4e displays mail from an old Maildir, check the Maildir stored in the `mu` database:

```sh
mu info
```

If necessary, reinitialize it:

```sh
mu init --maildir=~/Maildir --personal-address=user@example.com
mu index
```

Also verify that the local mail directory configured for the MailSalonSync account points to the same Maildir.

All three components should agree:

```text
MailSalonSync --> ~/Maildir
mu            --> ~/Maildir
mu4e          --> ~/Maildir
```
