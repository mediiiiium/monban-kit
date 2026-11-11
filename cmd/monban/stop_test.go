package main

import (
	"bufio"
	"bytes"
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newTestServer は、試し用の置き場にソケットを開き、接続を handle に回す門番の一部を作る。
func newTestServer(t *testing.T, owner uint32) (*server, string, string) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "mbk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sd := filepath.Join(dir, "s")
	if err := os.Mkdir(sd, 0o750); err != nil {
		t.Fatal(err)
	}
	os.Chmod(sd, 0o750)
	sock := filepath.Join(sd, "m.sock")
	logPath := filepath.Join(dir, "audit.log")
	audit, _, err := openAudit(logPath, 0)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := listenChecked(sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &server{owner: owner, allow: &allowlist{}, audit: audit, stop: make(chan struct{}), ln: ln}
	srv.ctx, srv.cancel = context.WithCancel(context.Background())
	go func() {
		for {
			c, err := ln.AcceptUnix()
			if err != nil {
				return
			}
			srv.conns.Add(1)
			go func() { defer srv.conns.Done(); srv.handle(c) }()
		}
	}()
	return srv, sock, logPath
}

func TestKill(t *testing.T) {
	srv, sock, logPath := newTestServer(t, uint32(os.Getuid()))
	// 門番の代わりに、止め始めたら停止の行を書いてソケットを消す。
	go func() {
		<-srv.stop
		srv.conns.Wait()
		srv.audit.close(auditEntry{Result: "門番を止めた（" + srv.why + "）"})
		os.Remove(sock)
	}()
	var out bytes.Buffer
	if err := stopDaemon(&out, sock, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "門番は止まった") {
		t.Fatalf("出力: %s", out.String())
	}
	if srv.ctx.Err() == nil {
		t.Fatal("取り出し中の bws を止める合図が出ていない")
	}
	if !strings.HasPrefix(srv.why, "monban stop、uid ") {
		t.Fatalf("止めた理由: %q", srv.why)
	}
	srv.halt("2回目") // 2回呼んでも落ちず、最初の理由が残る
	if !strings.HasPrefix(srv.why, "monban stop") {
		t.Fatalf("理由が上書きされた: %q", srv.why)
	}
	b, _ := os.ReadFile(logPath)
	st, _, err := verifyBytes(b, 0)
	if err != nil || st.Lines != 2 || st.LastOp != opStop {
		t.Fatalf("ログ: %v %+v\n%s", err, st, b)
	}
	if !bytes.Contains(b, []byte(`"op":"kill"`)) || !bytes.Contains(b, []byte("止める要求を受けた")) {
		t.Fatalf("止める要求の行がない: %s", b)
	}
	// 止まった後は繋げない。それでも stopDaemon はエラーで返る（失効の手順は runStop が出す）。
	if err := stopDaemon(&out, sock, time.Second); err == nil || !strings.Contains(err.Error(), "繋げない") {
		t.Fatalf("止まった後: %v", err)
	}
}

func TestKillOtherUID(t *testing.T) {
	srv, sock, _ := newTestServer(t, uint32(os.Getuid())+1) // 持ち主ではない uid から繋いだことにする
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	c.Write([]byte(`{"op":"kill"}` + "\n"))
	line, _ := bufio.NewReader(c).ReadString('\n')
	c.Close()
	if !strings.HasPrefix(line, "NG ") || srv.stopped() {
		t.Fatalf("持ち主ではない uid の止める要求を通した: %q", line)
	}
	srv.halt("後片づけ")
}

// 止め始めた後の要求は、許可リストより前で断られ、bws まで届かない。
func TestStoppedDeniesBeforeBws(t *testing.T) {
	srv, sock, logPath := newTestServer(t, uint32(os.Getuid()))
	close(srv.stop) // 止め始めた（待ち受けはまだ開いている、という間の要求）
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	c.Write([]byte(`{"agent":"a","secret":"MONBAN_TEST","command":["/usr/bin/printenv"]}` + "\n"))
	line, _ := bufio.NewReader(c).ReadString('\n')
	c.Close()
	if !strings.HasPrefix(line, "NG ") {
		t.Fatalf("返事: %q", line)
	}
	srv.ln.Close()
	srv.conns.Wait()
	b, _ := os.ReadFile(logPath)
	if bytes.Contains(b, []byte("渡した")) || !bytes.Contains(b, []byte(`"result":"`+outStopped+`"`)) {
		t.Fatalf("止めた後に渡した: %s", b)
	}
}

// 取り出し中に止めると、bws を止め、取れていても渡さない。偽の bws は、数秒待ってから値を返す。
func TestKillDuringBws(t *testing.T) {
	srv, sock, logPath := newTestServer(t, uint32(os.Getuid()))
	dir := filepath.Dir(logPath)
	fake := filepath.Join(dir, "bws")
	os.WriteFile(filepath.Join(dir, "out.json"), []byte(`{"key":"MONBAN_TEST","value":"hello"}`), 0o600)
	os.WriteFile(fake, []byte("#!/bin/sh\nsleep 5\ncat "+filepath.Join(dir, "out.json")+"\n"), 0o700)
	srv.bws = &bwsClient{path: fake, token: "fake", home: dir}
	a, err := parseAllowlist([]byte(`{"secrets":[{"name":"MONBAN_TEST","id":"0a1b2c3d-0000-0000-0000-000000000000",` +
		`"allow":[{"command":"/usr/bin/printenv","agents":["*"],"approval":"none"}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	srv.allow = a

	// 止めなければ、偽の bws は値を返す（偽物が動くことを先に確かめる）。
	if v, err := srv.bws.get(context.Background(), "MONBAN_TEST", "0a1b2c3d-0000-0000-0000-000000000000"); err != nil || v != "hello" {
		t.Fatalf("偽の bws: %q %v", v, err)
	}

	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	c.Write([]byte(`{"agent":"a","secret":"MONBAN_TEST","command":["/usr/bin/printenv","MONBAN_TEST"]}` + "\n"))
	time.Sleep(500 * time.Millisecond) // bws が動き始めるまで
	t0 := time.Now()
	srv.halt("試し")
	line, _ := bufio.NewReader(c).ReadString('\n')
	c.Close()
	if !strings.HasPrefix(line, "NG ") {
		t.Fatalf("返事: %q", line)
	}
	if d := time.Since(t0); d > 3*time.Second {
		t.Fatalf("止めてから断るまで %v（bws を待っていた）", d)
	}
	srv.conns.Wait()
	b, _ := os.ReadFile(logPath)
	if bytes.Contains(b, []byte("渡した")) || bytes.Contains(b, []byte("hello")) || !bytes.Contains(b, []byte(`"result":"`+outStopped+`"`)) {
		t.Fatalf("ログ: %s", b)
	}
}
