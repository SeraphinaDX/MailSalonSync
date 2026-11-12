package syncer

import (
	"os"
	"path/filepath"
	"testing"

	"git.cerberusgames.ca/Starstreak/MailSalonSync/internal/config"
	"git.cerberusgames.ca/Starstreak/MailSalonSync/internal/jmap"
	"git.cerberusgames.ca/Starstreak/MailSalonSync/internal/maildir"
	"git.cerberusgames.ca/Starstreak/MailSalonSync/internal/state"
)

func TestReconcileJMAPMove(t *testing.T) {
	cases := []struct {
		name string
		outcome jmap.MoveOutcome
		collision bool
		trackedExists bool
		wantFileKey string
		wantBackup bool
		wantEntry bool
	}{
		{"normal move", jmap.MoveCompleted, false, false, "moved-key", false, true},
		{"already moved remotely", jmap.MoveAlreadyInDestination, false, false, "moved-key", false, true},
		{"destination collision", jmap.MoveCompleted, true, true, "tracked-key", true, true},
		{"destination missing tracked file", jmap.MoveCompleted, true, false, "moved-key", false, true},
		{"remote deletion", jmap.MoveGone, true, true, "tracked-key", true, true},
		{"remote move elsewhere", jmap.MoveMovedElsewhere, false, false, "", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			cfg := &config.Config{StateDir: filepath.Join(root, "state")}
			a := &config.Account{Name:"test", LocalRoot:filepath.Join(root,"mail")}
			dest := config.Mailbox{Remote:"role:trash", Local:"Deleted Items"}
			destDir := maildir.Open(localMailboxPath(a,dest.Local))
			if _, err := destDir.Put("moved-key", []byte("Subject: moved\r\n\r\nbody"), true); err != nil { t.Fatal(err) }
			oldKey := state.Key("jmap","role:junk","email1")
			newKey := state.Key("jmap",dest.Remote,"email1")
			s := state.New()
			e := state.Entry{Protocol:"jmap",RemoteMailbox:"role:junk",RemoteID:"email1",LocalMailbox:"Junk Mail",FileKey:"moved-key"}
			s.Entries[oldKey]=e
			if tc.collision {
				tracked := state.Entry{Protocol:"jmap",RemoteMailbox:dest.Remote,RemoteID:"email1",LocalMailbox:dest.Local,FileKey:"tracked-key"}
				s.Entries[newKey]=tracked
				if tc.trackedExists {
					if _,err:=destDir.Put("tracked-key",[]byte("Subject: tracked\r\n\r\nbody"),true);err!=nil {t.Fatal(err)}
				}
			}
			statePath := filepath.Join(cfg.StateDir,"test.json")
			if err:=reconcileJMAPMove(cfg,a,s,statePath,oldKey,e,dest,tc.outcome);err!=nil {t.Fatal(err)}
			if _,ok:=s.Entries[oldKey];ok {t.Fatal("stale source state remains")}
			entry,ok:=s.Entries[newKey]
			if ok!=tc.wantEntry {t.Fatalf("destination state exists=%v, want %v",ok,tc.wantEntry)}
			if ok && entry.FileKey!=tc.wantFileKey {t.Fatalf("destination key = %q, want %q",entry.FileKey,tc.wantFileKey)}
			_,movedExists,err:=destDir.Find("moved-key")
			if err!=nil {t.Fatal(err)}
			if movedExists==tc.wantBackup {t.Fatalf("active moved copy exists=%v, want backup=%v",movedExists,tc.wantBackup)}
			if tc.wantBackup {
					backupRoot:=filepath.Join(cfg.StateDir,"adopt-backup","test","Deleted Items")
					found:=false
					err:=filepath.Walk(backupRoot,func(path string, info os.FileInfo, walkErr error)error {
						if walkErr!=nil{return walkErr}
						if !info.IsDir() {
							b,err:=os.ReadFile(path);if err!=nil{return err}
							if string(b)=="Subject: moved\r\n\r\nbody" {found=true}
						}
						return nil
					})
					if err!=nil {t.Fatal(err)}
					if !found {t.Fatal("local moved message not preserved in backup")}
				}
			reloaded,err:=state.Load(statePath)
			if err!=nil {t.Fatal(err)}
			if _,ok:=reloaded.Entries[oldKey];ok {t.Fatal("stale source state persisted")}
			if _,ok:=reloaded.Entries[newKey];ok!=tc.wantEntry {t.Fatalf("persisted destination state exists=%v",ok)}
		})
	}
}
