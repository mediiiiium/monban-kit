package main

// 2段目（ソケット）：daemon がソケットを開き、繋いできた相手の uid と pid を取る。
// uid が設定の持ち主と違えば断る。秘密はまだ扱わない。

import (
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// 置き場のディレクトリとソケットのモード（Muse と決めた形）。
// ディレクトリにグループの書き込みを付けると、ソケットを偽物に差し替えられる。
const (
	dirMode  fs.FileMode = 0o750
	sockMode fs.FileMode = 0o660
)

// defaultSocket は本番（常駐化の段）での置き場。試すときは -socket で変える。
const defaultSocket = "/var/run/monban/monban.sock"

func runDaemon(args []string) error {
	fl := flag.NewFlagSet("daemon", flag.ContinueOnError)
	sockPath := fl.String("socket", defaultSocket, "ソケットの置き場")
	ownerUID := fl.Int("owner-uid", -1, "繋いでよい人の uid（必須）")
	if err := fl.Parse(args); err != nil {
		return err
	}
	if *ownerUID < 0 {
		return errors.New("-owner-uid を指定してください")
	}

	ln, err := listenChecked(*sockPath)
	if err != nil {
		return err
	}
	// 止めるときにソケットを消す。
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sig
		ln.Close()
	}()
	defer os.Remove(*sockPath)

	log.Printf("待ち受け開始 socket=%s owner-uid=%d", *sockPath, *ownerUID)
	for {
		c, err := ln.AcceptUnix()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				log.Printf("止めた")
				return nil
			}
			return err
		}
		go handle(c, uint32(*ownerUID))
	}
}

// listenChecked は、置き場の持ち主とモードを調べてからソケットを開く。
// 想定と違えば開かない（fail closed）。
func listenChecked(sockPath string) (*net.UnixListener, error) {
	if !filepath.IsAbs(sockPath) {
		return nil, fmt.Errorf("ソケットの置き場は絶対パスにしてください: %s", sockPath)
	}
	// macOS のソケットのパスは 104 バイト（終端を含む）まで。
	if len(sockPath) > 103 {
		return nil, fmt.Errorf("ソケットのパスが長すぎる（103 バイトまで）: %d バイト", len(sockPath))
	}
	dir := filepath.Dir(sockPath)
	me := uint32(os.Geteuid())

	dst, err := os.Lstat(dir)
	if err != nil {
		return nil, fmt.Errorf("置き場のディレクトリ: %w", err)
	}
	if !dst.IsDir() {
		return nil, fmt.Errorf("置き場がディレクトリではない（シンボリックリンクも不可）: %s", dir)
	}
	dsys := dst.Sys().(*syscall.Stat_t)
	if dsys.Uid != me {
		return nil, fmt.Errorf("置き場の持ち主が自分（uid %d）ではない: uid %d", me, dsys.Uid)
	}
	if dst.Mode().Perm() != dirMode {
		return nil, fmt.Errorf("置き場のモードが %04o ではない: %04o", dirMode, dst.Mode().Perm())
	}

	// 前回の残りのソケットだけは消してよい。ほかのものがあれば開かない。
	if st, err := os.Lstat(sockPath); err == nil {
		if st.Mode().Type() != fs.ModeSocket || st.Sys().(*syscall.Stat_t).Uid != me {
			return nil, fmt.Errorf("ソケットの場所に別のものがある: %s", sockPath)
		}
		if err := os.Remove(sockPath); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}

	// 作った瞬間から 0660 になるよう、umask を先に絞る。
	old := unix.Umask(0o117)
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: sockPath, Net: "unix"})
	unix.Umask(old)
	if err != nil {
		return nil, err
	}
	ln.SetUnlinkOnClose(false) // 消すのは runDaemon の defer に任せる

	// できたソケットも確かめる。グループはディレクトリから引き継ぐ。
	sst, err := os.Lstat(sockPath)
	if err != nil {
		ln.Close()
		return nil, err
	}
	ssys := sst.Sys().(*syscall.Stat_t)
	if ssys.Uid != me || sst.Mode().Perm() != sockMode || ssys.Gid != dsys.Gid {
		ln.Close()
		os.Remove(sockPath)
		return nil, fmt.Errorf("できたソケットが想定と違う: uid %d gid %d モード %04o", ssys.Uid, ssys.Gid, sst.Mode().Perm())
	}
	return ln, nil
}

// peer は、繋いできたプロセスの uid と pid。カーネルが教えるので、相手は偽れない。
// ただし uid までで、どのエージェントかはわからない。
type peer struct {
	uid uint32
	pid int
}

func peerOf(c *net.UnixConn) (peer, error) {
	raw, err := c.SyscallConn()
	if err != nil {
		return peer{}, err
	}
	var p peer
	var serr error
	err = raw.Control(func(fd uintptr) {
		cred, e := unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
		if e != nil {
			serr = e
			return
		}
		pid, e := unix.GetsockoptInt(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERPID)
		if e != nil {
			serr = e
			return
		}
		p = peer{uid: cred.Uid, pid: pid}
	})
	if err != nil {
		return peer{}, err
	}
	return p, serr
}

func handle(c *net.UnixConn, owner uint32) {
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))

	p, err := peerOf(c)
	if err != nil {
		log.Printf("相手を確かめられない、断った: %v", err)
		return
	}
	if p.uid != owner {
		log.Printf("断った uid=%d pid=%d（持ち主ではない）", p.uid, p.pid)
		fmt.Fprintln(c, "NG 持ち主ではない")
		return
	}
	log.Printf("通した uid=%d pid=%d", p.uid, p.pid)
	fmt.Fprintln(c, "OK")
}
