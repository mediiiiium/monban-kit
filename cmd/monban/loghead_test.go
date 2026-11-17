package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 控えと合うログなら通し、消えた・短くなった・入れ替わったなら止める。止めるときはログを作らない。
func TestLogHead(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "audit.log")
	head := filepath.Join(dir, logHeadName)

	// 控えがない（初めて）なら通す。
	if refuseIfLogLost(head, logPath, nil) {
		t.Fatal("控えがないのに止めた")
	}
	writeSome(t, logPath, 3)
	b, _ := os.ReadFile(logPath)
	st, h2, err := verifyBytes(b, 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeLogHead(head, st.Lines, st.Last); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(head); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("控え: %v %v", fi, err)
	}
	if _, err := os.Lstat(head + ".tmp"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("一時ファイルが残った")
	}
	if refuseIfLogLost(head, logPath, nil) {
		t.Fatal("合っているのに止めた")
	}
	// ログが伸びた（控えが古い）なら通す。
	a, _, err := openAudit(logPath, 0)
	if err != nil {
		t.Fatal(err)
	}
	a.write(auditEntry{Result: "足した"})
	a.f.Close()
	if refuseIfLogLost(head, logPath, nil) {
		t.Fatal("伸びただけなのに止めた")
	}

	// 控えのハッシュが違う（入れ替わった）。
	writeLogHead(head, 2, st.Last)
	if err := checkLogHead(logPath, logHead{Seq: 2, Hash: st.Last}); err == nil || !strings.Contains(err.Error(), "控えと違う") {
		t.Fatalf("入れ替わりに気づかない: %v", err)
	}
	if err := checkLogHead(logPath, logHead{Seq: 2, Hash: h2}); err != nil {
		t.Fatal(err)
	}
	// 控えより短い。
	if err := checkLogHead(logPath, logHead{Seq: 9, Hash: st.Last}); err == nil {
		t.Fatal("短くなったのに気づかない")
	}
	// ファイルごと消えた：止めて、空のログを作らない。
	writeLogHead(head, st.Lines, st.Last)
	os.Remove(logPath)
	if !refuseIfLogLost(head, logPath, nil) {
		t.Fatal("ログが消えたのに止めなかった")
	}
	if _, err := os.Lstat(logPath); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("止めたのにログを作った")
	}
	// 空のログに入れ替わった（前の門番のように、空のファイルが作られた）。
	os.WriteFile(logPath, nil, 0o600)
	if !refuseIfLogLost(head, logPath, nil) {
		t.Fatal("空のログで止めなかった")
	}
}

// 控えは 0600 の自分の持ち物に限り、シンボリックリンクはたどらない。
func TestReadLogHeadStrict(t *testing.T) {
	dir := t.TempDir()
	head := filepath.Join(dir, logHeadName)
	if err := writeLogHead(head, 1, zeroHash); err != nil {
		t.Fatal(err)
	}
	if h, ok, err := readLogHead(head); err != nil || !ok || h.Seq != 1 {
		t.Fatalf("%+v %v %v", h, ok, err)
	}
	os.Chmod(head, 0o644)
	if _, _, err := readLogHead(head); err == nil {
		t.Fatal("0644 を受け付けた")
	}
	link := filepath.Join(dir, "link")
	os.Chmod(head, 0o600)
	os.Symlink(head, link)
	if _, _, err := readLogHead(link); err == nil {
		t.Fatal("シンボリックリンクをたどった")
	}
	if err := writeLogHead(link+"x", 1, zeroHash); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(head, []byte("でたらめ"), 0o600)
	if _, _, err := readLogHead(head); err == nil {
		t.Fatal("読めない控えを受け付けた")
	}
	// 控えが読めないときも、起動しない側に倒す。
	if !refuseIfLogLost(head, filepath.Join(dir, "audit.log"), nil) {
		t.Fatal("読めない控えで止めなかった")
	}
}

// 止めるときは、停止の行の後に控えを置き換える（次の起動は、それと照らす）。
func TestShutdownWritesLogHead(t *testing.T) {
	srv, sock, logPath := newTestServer(t, uint32(os.Getuid()))
	state := filepath.Join(filepath.Dir(logPath), "state")
	os.Mkdir(state, 0o700)
	srv.headPath = filepath.Join(state, logHeadName)
	srv.halt("合図 terminated", false)
	srv.shutdown(filepath.Join(state, stoppedName), sock)
	h, ok, err := readLogHead(srv.headPath)
	if err != nil || !ok {
		t.Fatalf("控えがない: %v", err)
	}
	b, _ := os.ReadFile(logPath)
	st, _, _ := verifyBytes(b, 0)
	if h.Seq != st.Lines || h.Hash != st.Last || st.LastOp != opStop {
		t.Fatalf("控え %+v、ログ %+v", h, st)
	}
	if refuseIfLogLost(srv.headPath, logPath, nil) {
		t.Fatal("止めた直後のログで止めた")
	}
}
