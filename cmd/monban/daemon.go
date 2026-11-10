package main

// daemon：ソケットを開き、繋いできた相手の uid と pid を取る。
// uid が設定の持ち主と違えば断る（2段目）。
// 3段目：要求された秘密を bws で取り出し、1行のログを足す。値はまだ run に渡さない。

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
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

type server struct {
	owner   uint32
	bws     *bwsClient
	secrets map[string]string
	audit   *auditLog
}

func runDaemon(args []string) error {
	fl := flag.NewFlagSet("daemon", flag.ContinueOnError)
	sockPath := fl.String("socket", defaultSocket, "ソケットの置き場")
	ownerUID := fl.Int("owner-uid", -1, "繋いでよい人の uid（必須）")
	bwsPath := fl.String("bws", "", "bws の絶対パス（必須）")
	tokenFile := fl.String("token-file", "", "合言葉のファイル（必須。0700 のディレクトリの中の 0600）")
	secretsFile := fl.String("secrets", "", "秘密の名前と ID の対応表（必須）")
	logFile := fl.String("log", "", "ログのファイル（必須）")
	if err := fl.Parse(args); err != nil {
		return err
	}
	if *ownerUID < 0 {
		return errors.New("-owner-uid を指定してください")
	}
	for _, f := range []struct{ name, v string }{
		{"-bws", *bwsPath}, {"-token-file", *tokenFile}, {"-secrets", *secretsFile}, {"-log", *logFile},
	} {
		if f.v == "" {
			return fmt.Errorf("%s を指定してください", f.name)
		}
	}

	bwsReal, err := checkBws(*bwsPath)
	if err != nil {
		return err
	}
	secrets, err := loadSecrets(*secretsFile)
	if err != nil {
		return err
	}
	token, home, err := readToken(*tokenFile)
	if err != nil {
		return err
	}
	audit, err := openAudit(*logFile)
	if err != nil {
		return err
	}
	srv := &server{
		owner:   uint32(*ownerUID),
		bws:     &bwsClient{path: bwsReal, token: token, home: home},
		secrets: secrets,
		audit:   audit,
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

	log.Printf("待ち受け開始 socket=%s owner-uid=%d bws=%s 秘密の数=%d", *sockPath, *ownerUID, bwsReal, len(secrets))
	for {
		c, err := ln.AcceptUnix()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				log.Printf("止めた")
				return nil
			}
			return err
		}
		go srv.handle(c)
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

// request は run から届く1行の JSON。形は4段目（許可リスト）で決め直す。
type request struct {
	Agent  string `json:"agent"`  // 申告されたエージェント名（名札）
	Secret string `json:"secret"` // 秘密の名前
}

const maxRequest = 4096

func (s *server) handle(c *net.UnixConn) {
	defer c.Close()
	c.SetDeadline(time.Now().Add(30 * time.Second))

	p, err := peerOf(c)
	if err != nil {
		log.Printf("相手を確かめられない、断った: %v", err)
		return
	}
	e := auditEntry{UID: p.uid, PID: p.pid}
	reply := func(result, msg string) {
		e.Result = result
		if err := s.audit.write(e); err != nil {
			// ログに残せないなら、通さない。
			log.Printf("ログに書けない、断った: %v", err)
			msg = "NG ログに書けない"
		}
		fmt.Fprintln(c, msg)
	}

	if p.uid != s.owner {
		log.Printf("断った uid=%d pid=%d（持ち主ではない）", p.uid, p.pid)
		reply("持ち主ではない", "NG 持ち主ではない")
		return
	}

	line, err := bufio.NewReader(io.LimitReader(c, maxRequest)).ReadBytes('\n')
	if err != nil {
		log.Printf("断った uid=%d pid=%d（要求を読めない）", p.uid, p.pid)
		reply("要求を読めない", "NG 要求を読めない")
		return
	}
	var req request
	if err := json.Unmarshal(line, &req); err != nil {
		reply("要求を読めない", "NG 要求を読めない")
		return
	}
	e.Agent = clip(req.Agent, 64)
	e.Secret = clip(req.Secret, 64)
	id, ok := s.secrets[req.Secret]
	if !ok {
		log.Printf("断った uid=%d pid=%d（対応表にない秘密）", p.uid, p.pid)
		reply("対応表にない秘密", "NG 対応表にない秘密")
		return
	}

	value, err := s.bws.get(context.Background(), req.Secret, id)
	if err != nil {
		log.Printf("断った uid=%d pid=%d secret=%s: %v", p.uid, p.pid, req.Secret, err)
		reply("bws が失敗", "NG bws が失敗した")
		return
	}
	// 3段目では値を渡さない（渡すのは5段目）。取れたことだけを返す。
	_ = value
	log.Printf("取れた uid=%d pid=%d secret=%s", p.uid, p.pid, req.Secret)
	reply("取れた", "OK 取れた（値はまだ渡さない）")
}

// clip は、ログに書く申告の文字列を n 文字までにする。
func clip(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
