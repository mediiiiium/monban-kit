package main

// daemon：ソケットを開き、繋いできた相手の uid と pid を取る。
// uid が設定の持ち主と違えば断る（2段目）。
// 3段目：要求された秘密を bws で取り出し、1行のログを足す。
// 4段目：許可リストで「どの秘密を・どのコマンドに・どの名札に」を照らす。
// 5段目：通した要求には、値を返事に載せて run に渡す（run が環境変数に入れて exec する）。
// 6段目：承認が要る項目は、待ちにして Pushover で知らせ、monban approve の後で初めて bws から取る。

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
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
	"strings"
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
	wait  waitList
	push  *pushover     // nil なら知らせず、番号は画面のログにだけ出す（試すとき用）
	stop  chan struct{} // 止めるときに閉じる（待っている要求を断る）
}

func runDaemon(args []string) error {
	fl := flag.NewFlagSet("daemon", flag.ContinueOnError)
	sockPath := fl.String("socket", defaultSocket, "ソケットの置き場")
	ownerUID := fl.Int("owner-uid", -1, "繋いでよい人の uid（必須）")
	bwsPath := fl.String("bws", "", "bws の絶対パス（必須）")
	tokenFile := fl.String("token-file", "", "合言葉のファイル（必須。0700 のディレクトリの中の 0600）")
	allowFile := fl.String("allowlist", "", "許可リスト（必須。JSON。本番は root の持ち物の 0644）")
	logFile := fl.String("log", "", "ログのファイル（必須）")
	pushFile := fl.String("pushover", "", "Pushover のトークンとユーザーキーの JSON（0700 のディレクトリの中の 0600）。なければ知らせず、承認の番号は画面のログにだけ出す")
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
	var others []string
	if *pushFile != "" && filepath.Dir(*pushFile) == filepath.Dir(*tokenFile) {
		others = append(others, filepath.Base(*pushFile))
	}
	token, home, err := readToken(*tokenFile, others...)
	if err != nil {
		return err
	}
	var push *pushover
	if *pushFile != "" {
		if push, err = readPushover(*pushFile); err != nil {
			return err
		}
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
		push:  push,
		stop:  make(chan struct{}),
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
		close(srv.stop)
		ln.Close()
	}()
	defer os.Remove(*sockPath)

	log.Printf("待ち受け開始 socket=%s owner-uid=%d bws=%s 秘密の数=%d Pushover=%v", *sockPath, *ownerUID, bwsReal, len(allow.secrets), push != nil)
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
	Agent   string   `json:"agent"`          // 申告されたエージェント名（名札）
	Secret  string   `json:"secret"`         // 秘密の名前
	Command []string `json:"command"`        // [絶対パス, 引数…]。run が PATH から引いて絶対パスにする
	Op      string   `json:"op,omitempty"`   // 空なら秘密の要求。"show"・"approve"・"deny" は承認の側（6段目）
	Code    string   `json:"code,omitempty"` // 承認の番号
}

const maxRequest = 16384

// maxValue は、run に渡す値の大きさの上限（バイト）。run の側も、これを元に読む量を絞る。
const maxValue = 65536

// okReply は、通したときの返事。値は base64 にして1行に収める（改行や空白を含む値でも行が崩れない）。
func okReply(id, value string) string {
	return "OK " + id + " " + base64.StdEncoding.EncodeToString([]byte(value)) + "\n"
}

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
	if req.Op != "" {
		e.Op = clip(req.Op, 16)
		s.handleDecision(c, e, p, req, deny)
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

	if rule.needsApproval() {
		if out := s.awaitApproval(c, e, req); out != outApproved {
			deny(out)
			return
		}
		// bws と返事の分だけ、締め切りを延ばし直す。
		c.SetDeadline(time.Now().Add(30 * time.Second))
	}

	value, err := s.bws.get(context.Background(), req.Secret, id)
	if err != nil {
		log.Printf("bws id=%s: %v", e.ID, err)
		deny("bws が失敗")
		return
	}
	// 環境変数に入れられない値（NUL を含む）と、大きすぎる値は渡さない。
	if strings.IndexByte(value, 0) >= 0 || len(value) > maxValue {
		deny("値を環境変数に入れられない（NUL を含むか、大きすぎる）")
		return
	}
	// ログに「渡した」と残せてから渡す。残せないなら、通さない。
	// ここで分かるのは「ソケットに書いた」まで。run が実際に exec したかは、門番からは見えない。
	e.Result = "渡した"
	if err := s.audit.write(e); err != nil {
		log.Printf("ログに書けない、断った id=%s: %v", e.ID, err)
		fmt.Fprintln(c, "NG "+e.ID)
		return
	}
	if _, err := io.WriteString(c, okReply(e.ID, value)); err != nil {
		log.Printf("渡せなかった id=%s: %v", e.ID, err)
		return
	}
	log.Printf("渡した id=%s uid=%d pid=%d secret=%s command=%s 引数の数=%d", e.ID, p.uid, p.pid, req.Secret, e.Command, e.Argc)
}

// clip は、ログに書く申告の文字列を n 文字までにする。
func clip(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// awaitApproval は、要求を待ちに入れて知らせ、結果が決まるまで待つ。値はまだ取っていない。
// 締め切りを延ばすのは、待ちに入ったこの接続だけ。
func (s *server) awaitApproval(c *net.UnixConn, e auditEntry, req request) string {
	pd := &pending{id: e.ID, agent: req.Agent, secret: req.Secret, command: req.Command, user: e.UserOwned, pid: e.PID, since: time.Now()}
	if err := s.wait.add(pd); err != nil {
		return "待ちが多すぎる"
	}
	if s.push != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		err := s.push.send(ctx, pd)
		cancel()
		if err != nil {
			s.wait.mu.Lock()
			s.wait.remove(pd)
			s.wait.mu.Unlock()
			log.Printf("知らせられない id=%s: %v", e.ID, err)
			return "Pushover で知らせられない"
		}
	}
	log.Printf("承認待ち id=%s 番号=%s secret=%s command=%s 引数の数=%d", e.ID, pd.code, req.Secret, e.Command, e.Argc)
	c.SetDeadline(time.Now().Add(approvalTimeout + 30*time.Second))
	// run には ID だけを返す（番号は返さない）。
	if _, err := fmt.Fprintf(c, "WAIT %s\n", e.ID); err != nil {
		s.wait.mu.Lock()
		ok := s.wait.remove(pd)
		s.wait.mu.Unlock()
		if ok {
			return outGone
		}
		return <-pd.done
	}
	// run は要求の後に何も送らない。読めたら（切れたら）、見捨てられたとみなす。
	gone := make(chan struct{})
	go func() {
		var b [1]byte
		c.Read(b[:])
		close(gone)
	}()
	return s.wait.wait(pd, approvalTimeout, gone, s.stop)
}
