//go:build windows

package config

import (
	"errors"
	"fmt"

	"golang.org/x/sys/windows"
)

func collectionPIDAlive(pid int) (bool, error) {
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
			return false, nil
		}
		return true, err // Access denied does not prove process death.
	}
	defer windows.CloseHandle(h)
	status, err := windows.WaitForSingleObject(h, 0)
	if err != nil {
		return true, err
	}
	switch status {
	case windows.WAIT_OBJECT_0:
		return false, nil
	case windows.WAIT_TIMEOUT:
		return true, nil
	default:
		return true, fmt.Errorf("unexpected process wait status %d", status)
	}
}
