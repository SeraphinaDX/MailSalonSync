package config

import (
 "bufio"
 "fmt"
 "os"
 "os/exec"
 "path/filepath"
 "runtime"
 "strings"
 "testing"
 "time"
)

func TestCollectionLockContentionAndLegacy(t *testing.T) {
 dir := t.TempDir()
 unlock, err := LockCollection(dir)
 if err != nil { t.Fatal(err) }
 if _, err := LockCollection(dir); err == nil { t.Fatal("overlapping collection lock accepted") }
 unlock()
 unlock()
 if _, err := os.Stat(filepath.Join(dir, ".mss-lock")); !os.IsNotExist(err) { t.Fatalf("lock file left after release: %v", err) }
 if _, err := os.Stat(filepath.Join(dir, ".mss-lock.guard")); err != nil { t.Fatalf("guard missing: %v", err) }
 if err := os.WriteFile(filepath.Join(dir, ".mss-lock"), nil, 0600); err != nil { t.Fatal(err) }
 if _, err := LockCollection(dir); err == nil || !strings.Contains(err.Error(), "legacy") { t.Fatalf("legacy lock was reclaimed: %v", err) }
}

func TestCollectionLockKilledOwner(t *testing.T) {
 const env = "MAILSALONSYNC_TEST_COLLECTION_CHILD"
 if dir := os.Getenv(env); dir != "" {
  release, err := LockCollection(dir)
  if err != nil { fmt.Println(err); os.Exit(1) }
  fmt.Println("LOCKED")
  time.Sleep(time.Hour)
  release()
  return
 }
 switch runtime.GOOS { case "linux", "darwin", "freebsd", "netbsd", "openbsd", "dragonfly", "windows": default: t.Skip("unsupported platform") }
 dir := t.TempDir()
 exe, err := os.Executable()
 if err != nil { t.Fatal(err) }
 cmd := exec.Command(exe, "-test.run=^TestCollectionLockKilledOwner$")
 cmd.Env = append(os.Environ(), env+"="+dir)
 pipe, err := cmd.StdoutPipe()
 if err != nil { t.Fatal(err) }
 if err := cmd.Start(); err != nil { t.Fatal(err) }
 defer func() { if cmd.ProcessState == nil { _ = cmd.Process.Kill(); _ = cmd.Wait() } }()
 ready := make(chan string, 1)
 go func() { scanner := bufio.NewScanner(pipe); if scanner.Scan() { ready <- scanner.Text() } else { ready <- "child exited" } }()
 select {
 case line := <-ready: if line != "LOCKED" { t.Fatalf("child failed: %s", line) }
 case <-time.After(5*time.Second): t.Fatal("child timeout")
 }
 if _, err := LockCollection(dir); err == nil { t.Fatal("overlapping sync accepted") }
 if err := cmd.Process.Kill(); err != nil { t.Fatal(err) }
 _ = cmd.Wait()
 unlock, err := LockCollection(dir)
 if err != nil { t.Fatalf("failed to recover killed owner: %v", err) }
 unlock()
}
