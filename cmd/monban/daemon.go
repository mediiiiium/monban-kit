package main

// daemon：ソケットを開き、繋いできた相手の uid と pid を取る。
// uid が設定の持ち主と違えば断る（2段目）。
// 3段目：要求された秘密を bws で取り出し、1行のログを足す。
// 4段目：許可リストで「どの秘密を・どのコマンドに・どの名札に」を照らす。値はまだ run に渡さない。

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
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
	owner uint32
	bws   *bwsClient
	allow *allowlist
	audit *auditLog
}

func runDaemon(args []string) error {
	fl := flag.NewFlagSet("daemon", flag.ContinueOnError)
	sockPath := fl.String("socket", defaultSocket, "ソケットの置き場")
	ownerUID := fl.Int("owner-uid", -1, "繋いでよい人の uid（必須）")
	bwsPath := fl.String("bws", "", "bws の絶対パス（必須）")
	tokenFile := fl.String("token-file", "", "合言葉のファイル（必須。0700 のディレクトリの中の 0600）")
	allowFile := fl.String("allowlist", "", "許可リスト（必須。JSON。本番は root の持ち物の 0644）")
	logFile := fl.String("log", "", "ログのファイル（必須）")
	if err := fl.Parse(args); err != nil {
		return err
	}
	if *ownerUID < 0 {
		return errors.New("-owner-uid を指定してください")
	}
	for _, f := range []struct{ name, v string }{
		{"-bws", *bwsPath}, {"-token-file", *tokenFile}, {"-allowlist", *allowFile}, {"-log", *logFile},
	} {
		if f.v == "" {
			return fmt.Errorf("%s を指定してください", f.name)
		}
	}

	bwsReal, err := checkBws(*bwsPath)
	if err != nil {
		return err
	}
	allow, err := loadAllowlist(*allowFile)
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
		owner: uint32(*ownerUID),
		bws:   &bwsClient{path: bwsReal, token: token, home: home},
		allow: allow,
		audit: audit,
	}
	// ユーザーの持ち物の場所のコマンドを許しているなら、本人が選んだ緩さとして画面とログに残す。
	for _, w := range allow.weakRules() {
		log.Printf("注意：ユーザーの持ち物の場所のコマンドを許している secret=%s command=%s", w[0], w[1])
		if err := audit.write(auditEntry{UID: uint32(os.Geteuid()), PID: os.Getpid(), Secret: w[0], Command: w[1],
			Result: "起動：ユーザーの持ち物の場所のコマンドを許している"}); err != nil {
			return fmt.Errorf("ログに書けない: %w", err)
		}
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

	log.Printf("待ち受け開始 socket=%s owner-uid=%d bws=%s 秘密の数=%d", *sockPath, *ownerUID, bwsReal, len(allow.secrets))
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

// request は run から届く1行の JSON。名札もコマンドも申告（認証ではない）。
type request struct {
	Agent   string   `json:"agent"`   // 申告されたエージェント名（名札）
	Secret  string   `json:"secret"`  // 秘密の名前
	Command []string `json:"command"` // [絶対パス, 引数…]。run が PATH から引いて絶対パスにする
}

const maxRequest = 16384

// newRequestID は、人がログと照らすためのリクエストの ID。
func newRequestID() string {
	b := make([]byte, 6)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// argsHash は、引数全体のハッシュ。引数には別の秘密が入りうるので、ログには全文を書かない。
func argsHash(args []string) string {
	b, _ := json.Marshal(args)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func (s *server) handle(c *net.UnixConn) {
	defer c.Close()
	c.SetDeadline(time.Now().Add(30 * time.Second))

	p, err := peerOf(c)
	if err != nil {
		log.Printf("相手を確かめられない、断った: %v", err)
		return
	}
	e := auditEntry{ID: newRequestID(), UID: p.uid, PID: p.pid}
	// 断るときの返事は一律に「NG ID」。詳しい理由はログにだけ書く（許可リストの中身を探られないように）。
	deny := func(reason string) {
		e.Result = reason
		log.Printf("断った id=%s uid=%d pid=%d secret=%q command=%q（%s）", e.ID, p.uid, p.pid, e.Secret, e.Command, reason)
		if err := s.audit.write(e); err != nil {
			log.Printf("ログに書けない: %v", err)
		}
		fmt.Fprintln(c, "NG "+e.ID)
	}

	if p.uid != s.owner {
		deny("持ち主ではない")
		return
	}

	line, err := bufio.NewReader(io.LimitReader(c, maxRequest)).ReadBytes('\n')
	if err != nil {
		deny("要求を読めない")
		return
	}
	var req request
	if err := json.Unmarshal(line, &req); err != nil {
		deny("要求を読めない")
		return
	}
	e.Agent = clip(req.Agent, 64)
	e.Secret = clip(req.Secret, 64)
	if len(req.Command) > 0 {
		e.Command = clip(req.Command[0], 256)
		e.Argc = len(req.Command) - 1
		e.ArgsSHA256 = argsHash(req.Command[1:])
	}
	id, rule, why := s.allow.check(req.Agent, req.Secret, req.Command)
	if why != "" {
		deny(why)
		return
	}
	e.UserOwned = rule.UserOwned

	value, err := s.bws.get(context.Background(), req.Secret, id)
	if err != nil {
		log.Printf("bws id=%s: %v", e.ID, err)
		deny("bws が失敗")
		return
	}
	// 4段目でも値は渡さない（渡すのは5段目）。取れたことだけを返す。
	_ = value
	e.Result = "取れた"
	if err := s.audit.write(e); err != nil {
		// ログに残せないなら、通さない。
		log.Printf("ログに書けない、断った id=%s: %v", e.ID, err)
		fmt.Fprintln(c, "NG "+e.ID)
		return
	}
	log.Printf("取れた id=%s uid=%d pid=%d secret=%s command=%s 引数の数=%d", e.ID, p.uid, p.pid, req.Secret, e.Command, e.Argc)
	fmt.Fprintln(c, "OK "+e.ID+" 取れた（値はまだ渡さない）")
}

// clip は、ログに書く申告の文字列を n 文字までにする。
func clip(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
