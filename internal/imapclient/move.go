package imapclient

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
)

var copyUIDRE = regexp.MustCompile(`(?i)\[COPYUID\s+[0-9]+\s+[0-9:,]+\s+([0-9:,]+)\]`)

// MoveUID moves one message from the currently selected mailbox into dest and
// returns the destination UID. UIDPLUS is required so MailSalonSync can update
// its state without guessing the destination identity.
func (c *Client) MoveUID(uid uint32, dest string) (uint32, error) {
	if c.selected == "" {
		return 0, errors.New("no IMAP mailbox selected")
	}
	if dest == "" {
		return 0, errors.New("destination IMAP mailbox is empty")
	}
	if !c.HasCapability("UIDPLUS") {
		return 0, errors.New("server lacks UIDPLUS; cannot safely track destination UID for a local Maildir move")
	}

	var (
		line string
		err  error
	)
	if c.HasCapability("MOVE") {
		line, err = c.simple(fmt.Sprintf("UID MOVE %d %s", uid, quote(dest)), nil)
		if err != nil {
			return 0, err
		}
		return parseCopyUID(line)
	}

	line, err = c.simple(fmt.Sprintf("UID COPY %d %s", uid, quote(dest)), nil)
	if err != nil {
		return 0, err
	}
	newUID, err := parseCopyUID(line)
	if err != nil {
		return 0, err
	}
	if err := c.DeleteUID(uid, false); err != nil {
		return 0, err
	}
	return newUID, nil
}

func parseCopyUID(line string) (uint32, error) {
	m := copyUIDRE.FindStringSubmatch(line)
	if len(m) != 2 {
		return 0, fmt.Errorf("IMAP move/copy response did not include COPYUID: %s", line)
	}
	if regexp.MustCompile(`[,:]`).MatchString(m[1]) {
		return 0, fmt.Errorf("IMAP COPYUID returned unexpected UID set %q for a single-message move", m[1])
	}
	n, err := strconv.ParseUint(m[1], 10, 32)
	if err != nil {
		return 0, fmt.Errorf("bad destination UID in COPYUID response %q", m[1])
	}
	return uint32(n), nil
}
