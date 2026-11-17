package main

// 対策 3：同時の接続の上限と、要求を読むまでの短い締め切り。

import (
	"bufio"
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func shrinkLimits(t *testing.T, n int, wait time.Duration) {
	t.Helper()
	oldN, oldW := maxConns, requestWait
	maxConns, requestWait = n, wait
	t.Cleanup(func() { maxConns, requestWait = oldN, oldW })
}

// 何も送らない接続は、30秒ではなく requestWait で切られる。
func TestIdleConnClosedSoon(t *testing.T) {
	shrinkLimits(t, 64, 200*time.Millisecond)
	_, sock, _ := newTestServer(t, uint32(os.Getuid()))
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(3 * time.Second))
	start := time.Now()
	buf := make([]byte, 64)
	for {
		if _, err := c.Read(buf); err != nil {
			break // 門番が閉じた
		}
	}
	if el := time.Since(start); el > 2*time.Second {
		t.Fatalf("切られるまで %v かかった", el)
	}
}

// 埋まっている間に来た接続は、断られずに待たされ、空いたら扱われる。
func TestConnLimitQueues(t *testing.T) {
	shrinkLimits(t, 2, 300*time.Millisecond)
	_, sock, _ := newTestServer(t, uint32(os.Getuid()))
	var idle []net.Conn
	for i := 0; i < 2; i++ {
		c, err := net.Dial("unix", sock)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		idle = append(idle, c)
	}
	time.Sleep(50 * time.Millisecond) // 2つとも受けられるまで
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	start := time.Now()
	if _, err := c.Write([]byte("{}\n")); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(c).ReadString('\n')
	if err != nil {
		t.Fatalf("返事が来ない: %v", err)
	}
	if !strings.HasPrefix(line, "NG ") {
		t.Fatalf("返事 %q", line)
	}
	// 埋まっていたので、先の2つが切られるまでは扱われていない。
	if el := time.Since(start); el < 200*time.Millisecond {
		t.Fatalf("埋まっているのに %v で扱われた", el)
	}
}

// 埋まって待っている間に止めると、serve は終わる。
func TestConnLimitStopWhileFull(t *testing.T) {
	shrinkLimits(t, 1, time.Minute)
	dir, err := os.MkdirTemp("/tmp", "mbk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	audit, _, err := openAudit(filepath.Join(dir, "audit.log"), 0)
	if err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, "x.sock")
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: sock, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	srv := &server{owner: uint32(os.Getuid()), allow: &allowlist{}, audit: audit, stop: make(chan struct{}), ln: ln}
	srv.ctx, srv.cancel = context.WithCancel(context.Background())
	defer srv.cancel()
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	done := make(chan error, 1)
	go func() { done <- srv.serve(ln) }()
	time.Sleep(100 * time.Millisecond) // 1つ受けて埋まり、空くのを待つところに入る
	close(srv.stop)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("止めても serve が終わらない")
	}
	c.Close() // 動いている接続を終わらせる
	srv.conns.Wait()
}
