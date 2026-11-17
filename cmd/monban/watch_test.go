package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// launchctl print の一番上の段だけを読む（下の段の state = active に引っ張られない）。
func TestLaunchdState(t *testing.T) {
	out := "system/io.github.mediiiiium.monban = {\n" +
		"\tactive count = 0\n" +
		"\tpath = /Library/LaunchDaemons/io.github.mediiiiium.monban.plist\n" +
		"\ttype = LaunchDaemon\n" +
		"\tstate = spawn scheduled\n" +
		"\n\tprogram = /usr/local/libexec/monban/monban\n" +
		"\truns = 4\n" +
		"\tlast exit code = 78: EX_CONFIG\n" +
		"\tendpoints = {\n\t\tstate = active\n\t}\n" +
		"}\n"
	st, le := launchdState(out)
	if st != "spawn scheduled" || le != "78: EX_CONFIG" {
		t.Fatalf("state=%q last=%q", st, le)
	}
	st, _ = launchdState("x = {\n\t\tstate = active\n\tstate = running\n}\n")
	if st != "running" {
		t.Fatalf("下の段を読んだ: %q", st)
	}
}

func TestWatchAction(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name                 string
		up, stopped, alerted bool
		ago                  time.Duration
		want                 string
	}{
		{"動いている", true, false, false, 0, ""},
		{"止まった・初めて", false, false, false, 0, "down"},
		{"止まった・知らせたばかり", false, false, true, time.Hour, ""},
		{"止まった・6時間たった", false, false, true, 6 * time.Hour, "down"},
		{"戻った", true, false, true, time.Hour, "back"},
		{"人が止めた", false, true, false, 0, ""},
		{"人が止めた・前に知らせていた", false, true, true, time.Hour, "clear"},
	}
	for _, c := range cases {
		if got := watchAction(now, c.up, c.stopped, c.alerted, now.Add(-c.ago), watchRealert); got != c.want {
			t.Errorf("%s: %q（想定 %q）", c.name, got, c.want)
		}
	}
}

// 知らせた印は 0600 で置き、シンボリックリンクはたどらない。
func TestTouchAlert(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, watchAlertName)
	if err := touchAlert(p); err != nil {
		t.Fatal(err)
	}
	st, err := os.Lstat(p)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("%v %v", st, err)
	}
	target := filepath.Join(dir, "target")
	os.WriteFile(target, nil, 0o600)
	link := filepath.Join(dir, "link")
	os.Symlink(target, link)
	if err := touchAlert(link); err == nil {
		t.Fatal("シンボリックリンクをたどった")
	}
}
