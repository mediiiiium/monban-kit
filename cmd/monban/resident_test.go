package main

// 9段目：止めた印と、本番の形での bws の扱い。

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// monban stop で止めると、停止の行の後に止めた印が置かれ、次の起動は1行足して断る。
func TestKillPlacesMarker(t *testing.T) {
	srv, sock, logPath := newTestServer(t, uint32(os.Getuid()))
	state := filepath.Join(filepath.Dir(logPath), "state")
	if err := os.Mkdir(state, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(state, stoppedName)
	go func() {
		<-srv.stop
		srv.shutdown(marker, sock)
	}()
	var out bytes.Buffer
	if err := stopDaemon(&out, sock, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	// stopDaemon が「止まった」と言った時点で、印はもうある。
	st, err := os.Lstat(marker)
	if err != nil || st.Mode().Perm() != 0o600 || !st.Mode().IsRegular() {
		t.Fatalf("止めた印: %v %v", st, err)
	}
	b, _ := os.ReadFile(marker)
	if !strings.Contains(string(b), "monban stop、uid ") || !strings.Contains(string(b), "seq 2") {
		t.Fatalf("印の中身: %s", b)
	}

	// 次の起動：印があるので、1行足して断る（正常に終わる）。
	audit, _, err := openAudit(logPath, 0)
	if err != nil {
		t.Fatal(err)
	}
	refused, err := refuseIfStopped(marker, audit, nil)
	if !refused || err != nil {
		t.Fatalf("印があるのに起動を断らなかった: %v %v", refused, err)
	}
	lb, _ := os.ReadFile(logPath)
	cs, _, err := verifyBytes(lb, 0)
	if err != nil || cs.Lines != 3 || cs.LastOp != opStop || !bytes.Contains(lb, []byte("止めた印があるので起動しなかった")) {
		t.Fatalf("ログ: %v %+v\n%s", err, cs, lb)
	}
	// もう一度起動しようとしても同じ（1行ずつ増える）。中身を偽っても、有無だけを見る。
	os.WriteFile(marker, []byte("でたらめ"), 0o600)
	audit, _, err = openAudit(logPath, 0)
	if err != nil {
		t.Fatal(err)
	}
	if refused, _ := refuseIfStopped(marker, audit, nil); !refused {
		t.Fatal("中身を変えた印で起動した")
	}
	// 印を消せば起動する。
	os.Remove(marker)
	audit, prior, err := openAudit(logPath, 0)
	if err != nil || prior.Lines != 4 || prior.LastOp != opStop {
		t.Fatalf("印を消した後: %v %+v", err, prior)
	}
	if refused, err := refuseIfStopped(marker, audit, nil); refused || err != nil {
		t.Fatalf("印がないのに断った: %v %v", refused, err)
	}
	audit.close(auditEntry{Result: "試しの後片づけ"})
}

// 合図で止めたときは、印を置かない（落ちたときと同じく、起き直してよい）。
func TestSignalNoMarker(t *testing.T) {
	srv, sock, logPath := newTestServer(t, uint32(os.Getuid()))
	state := filepath.Join(filepath.Dir(logPath), "state")
	os.Mkdir(state, 0o700)
	marker := filepath.Join(state, stoppedName)
	srv.halt("合図 terminated", false)
	srv.shutdown(marker, sock)
	if _, err := os.Lstat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("合図で止めたのに印がある: %v", err)
	}
	if _, err := os.Lstat(sock); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ソケットが残っている: %v", err)
	}
}

// 印を置く場所に、もう何かあれば（シンボリックリンクを含む）、上書きしない。
func TestMarkerNoOverwrite(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	os.WriteFile(target, []byte("元のまま"), 0o600)
	marker := filepath.Join(dir, stoppedName)
	os.Symlink(target, marker)
	if err := writeMarker(marker, "試し", 1, zeroHash); err == nil {
		t.Fatal("シンボリックリンクの先に書いた")
	}
	if b, _ := os.ReadFile(target); string(b) != "元のまま" {
		t.Fatalf("先のファイルが変わった: %s", b)
	}
}

// 普段の取り出しでも、時間切れが効く。bws が子を残して出力を握っていても、待たされ続けない。
// 本番の形（quiet）では、bws の stderr をエラーに入れない。
func TestBwsTimeoutNormalPath(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "bws")
	// 子の sleep が stdout と stderr を握ったまま残る形。
	os.WriteFile(fake, []byte("#!/bin/sh\necho 'Error: 合言葉 fake-token の話' >&2\nsleep 30\n"), 0o700)
	for _, quiet := range []bool{false, true} {
		b := &bwsClient{path: fake, token: "fake-token", home: dir, quiet: quiet, limit: time.Second}
		t0 := time.Now()
		_, err := b.get(context.Background(), "MONBAN_TEST", "0a1b2c3d-0000-0000-0000-000000000000")
		d := time.Since(t0)
		if err == nil || !errors.Is(err, errBws) {
			t.Fatalf("quiet=%v: 時間切れなのにエラーでない: %v", quiet, err)
		}
		if d > 4*time.Second {
			t.Fatalf("quiet=%v: 時間切れから %v 待たされた", quiet, d)
		}
		if strings.Contains(err.Error(), "fake-token") {
			t.Fatalf("quiet=%v: 合言葉がエラーに出た: %v", quiet, err)
		}
		if quiet && strings.Contains(err.Error(), "Error:") {
			t.Fatalf("本番の形なのに stderr が出た: %v", err)
		}
		if !quiet && !strings.Contains(err.Error(), "[伏せた]") {
			t.Logf("試しの形のエラー: %v", err)
		}
		t.Logf("quiet=%v %v: %v", quiet, d.Round(100*time.Millisecond), err)
	}
}
