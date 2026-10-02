package config

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"
)

func TestOperationLockReleasedAfterProcessKill(t *testing.T) {
	if root := os.Getenv("MAILSALONSYNC_LOCK_TEST_CHILD"); root != "" {
		c := &Config{StateDir: root}
		if _, err := c.LockOperations(); err != nil {
			fmt.Println(err)
			os.Exit(1)
		}
		fmt.Println("LOCKED")
		time.Sleep(time.Hour)
		os.Exit(0)
	}
	switch runtime.GOOS {
	case "linux", "darwin", "freebsd", "netbsd", "openbsd", "dragonfly", "windows":
	default:
		t.Skip("fallback platform")
	}
	root := t.TempDir()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^TestOperationLockReleasedAfterProcessKill$")
	cmd.Env = append(os.Environ(), "MAILSALONSYNC_LOCK_TEST_CHILD="+root)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	ready := make(chan string, 1)
	go func() { scanner := bufio.NewScanner(out); scanner.Scan(); ready <- scanner.Text() }()
	select {
	case line := <-ready:
		if line != "LOCKED" {
			t.Fatal(line)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("child did not lock")
	}
	cfg := &Config{StateDir: root}
	if _, err := cfg.LockOperations(); err == nil {
		t.Fatal("accepted another process")
	}
	cmd.Process.Kill()
	cmd.Wait()
	unlock, err := cfg.LockOperations()
	if err != nil {
		t.Fatal("killed process leaked its lock", err)
	}
	unlock()
}
