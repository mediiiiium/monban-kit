package main

// daemon：ソケットを開き、繋いできた相手の uid と pid を取る。
// uid が設定の持ち主と違えば断る（2段目）。
// 3段目：要求された秘密を bws で取り出し、1行のログを足す。
// 4段目：許可リストで「どの秘密を・どのコマンドに・どの名札に」を照らす。
// 5段目：通した要求には、値を返事に載せて run に渡す（run が環境変数に入れて exec する）。
// 6段目：承認が要る項目は、待ちにして Pushover で知らせ、monban approve の後で初めて bws から取る。
// 8段目：キルスイッチ。monban stop の接続（持ち主の uid から）で、新しい要求を受けるのをやめ、
// 待っている要求を断り、取り出し中の bws を止め、まだ渡していない値は渡さずに止まる。
// 9段目：常駐化。monban stop で止めたときは、状態のディレクトリに「止めた印」を置き、印があれば起動しない
// （ログに1行書いて、正常に終わる。launchd の KeepAlive が起き直させないように）。
// 本番の形（門番の euid が持ち主の uid と違う）では、Pushover と状態のディレクトリを必須にし、bws の stderr を出さない。

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
	"sync"
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
// /var/run の下には _monban がディレクトリを作れないので、消えない /var/db の下に、入れるときに作る（9段目、Muse と決めた）。
const defaultSocket = "/var/db/monban-run/monban.sock"

// stoppedName は、止めた印のファイルの名前（状態のディレクトリの中）。
const stoppedName = "stopped"

type server struct {
	owner uint32
	bws   *bwsClient
	allow *allowlist
	audit *auditLog
	wait  waitList
	push  *pushover      // nil なら知らせず、番号は画面のログにだけ出す（試すとき用）
	stop  chan struct{}  // 止めるときに閉じる（待っている要求を断る）
	conns sync.WaitGroup // 動いている接続。止めるときは、全部終わってから停止の行を書く

	ctx      context.Context // 止めるときに取り消す（取り出し中の bws を止める）
	cancel   context.CancelFunc
	ln       *net.UnixListener
	haltOnce sync.Once
	why      string // 何で止めたか（停止の行と知らせに書く）
	mark     bool   // 止めた印を置くか（monban stop で止めたときだけ）
}

// halt は、門番を止め始める。何度呼んでもよい（最初の理由だけを残す）。
// 新しい接続を受けるのをやめ、待っている要求を断り、取り出し中の bws を止める。
// mark は、止めた印を置くか（monban stop のときだけ。合図や落ちたときは置かない）。
func (s *server) halt(why string, mark bool) {
	s.haltOnce.Do(func() {
		s.why = why
		s.mark = mark
		close(s.stop)
		s.cancel()
		if s.ln != nil {
			s.ln.Close()
		}
	})
}

// stopped は、止め始めたか。
func (s *server) stopped() bool {
	select {
	case <-s.stop:
		return true
	default:
		return false
	}
}

func runDaemon(args []string) error {
	fl := flag.NewFlagSet("daemon", flag.ContinueOnError)
	sockPath := fl.String("socket", defaultSocket, "ソケットの置き場")
	ownerUID := fl.Int("owner-uid", -1, "繋いでよい人の uid（必須）")
	bwsPath := fl.String("bws", "", "bws の絶対パス（必須）")
	tokenFile := fl.String("token-file", "", "合言葉のファイル（必須。0700 のディレクトリの中の 0600）")
	allowFile := fl.String("allowlist", "", "許可リスト（必須。JSON。本番は root の持ち物の 0644）")
	logFile := fl.String("log", "", "ログのファイル（必須）")
	pushFile := fl.String("pushover", "", "Pushover のトークンとユーザーキーの JSON（0700 のディレクトリの中の 0600）。なければ知らせず、承認の番号は画面のログにだけ出す（本番の形では必須）")
	stateDir := fl.String("state-dir", "", "状態のディレクトリ（自分の持ち物の 0700）。止めた印を置く。なければ印を使わない（本番の形では必須）")
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

	// 本番の形：門番が持ち主とは別の uid（_monban）で動いている。
	// 承認の番号は、_monban しか読めない画面のログにしか出ないので、Pushover がないと承認できない。
	// キルスイッチを再起動の後も効かせるには、止めた印の置き場が要る。
	production := uint32(os.Geteuid()) != uint32(*ownerUID)
	if production && *pushFile == "" {
		return errors.New("本番の形（門番の uid が -owner-uid と違う）では -pushover が要る（ないと、承認の番号を受け取る道がない）")
	}
	if production && *stateDir == "" {
		return errors.New("本番の形（門番の uid が -owner-uid と違う）では -state-dir が要る（ないと、monban stop で止めても再起動で動き出す）")
	}
	var marker string
	if *stateDir != "" {
		if !filepath.IsAbs(*stateDir) {
			return fmt.Errorf("状態のディレクトリは絶対パスにしてください: %s", *stateDir)
		}
		if err := checkPrivate(*stateDir, 0o700, true); err != nil {
			return fmt.Errorf("状態のディレクトリ: %w", err)
		}
		marker = filepath.Join(*stateDir, stoppedName)
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
	// ログを 0640 にするときのグループは、ソケットの置き場と同じ専用グループに限る。
	sockDir, err := os.Lstat(filepath.Dir(*sockPath))
	if err != nil {
		return fmt.Errorf("ソケットの置き場: %w", err)
	}
	audit, prior, err := openAudit(*logFile, sockDir.Sys().(*syscall.Stat_t).Gid)
	if err != nil {
		return err
	}
	// 止めた印があれば起動しない。門番は「あるかないか」だけを見る（中身は人が読むための表示で、信じない）。
	// 連鎖の決まりどおりに1行足してから、正常に終わる（失敗で終わると、launchd の KeepAlive が起き直させ続ける）。
	if marker != "" {
		if refused, err := refuseIfStopped(marker, audit, push); refused || err != nil {
			return err
		}
	}
	// 止める合図の受け口は、起動の行を書く前に作る。後にすると、起動の行と停止の行の間に届いた合図で、
	// 停止の行を書かずに落ちる（届いた合図は sig に残り、待ち受けを始めたらすぐ止める）。
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	// 起動の行。前回の停止の行がないなら、落ちたか、ログの末尾が消された（区別はできない）。
	start := auditEntry{UID: uint32(os.Geteuid()), PID: os.Getpid(), Op: opStart, Result: "門番を起動した"}
	if prior.Lines > 0 && prior.LastOp != opStop {
		start.Result = "門番を起動した。前回の停止の行がない（落ちたか、ログの末尾が消された）"
		log.Printf("注意！！ ログの最後（seq %d）が停止の行ではない。前回の門番が落ちたか、ログの末尾が消された（区別はできない）", prior.Lines)
	}
	if err := audit.write(start); err != nil {
		return fmt.Errorf("ログに書けない: %w", err)
	}
	srv := &server{
		owner: uint32(*ownerUID),
		bws:   &bwsClient{path: bwsReal, token: token, home: home, quiet: production},
		allow: allow,
		audit: audit,
		push:  push,
		stop:  make(chan struct{}),
	}
	srv.ctx, srv.cancel = context.WithCancel(context.Background())
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
		// 起動の行を書いた後なので、停止の行も書いておく（次の起動で「停止の行がない」と出ないように）。
		audit.close(auditEntry{UID: uint32(os.Geteuid()), PID: os.Getpid(), Result: "待ち受けを始められずに止めた"})
		return err
	}
	// 起動の知らせに、ログの連鎖の今の先頭を載せる（門番の外の写し）。送れなくても動く。
	seq, h := audit.head()
	log.Printf("ログ seq %d ハッシュ %s", seq, h)
	srv.notify("monban 起動", fmt.Sprintf("門番を起動した\nログ seq %d\nハッシュ %s", seq, short(h)))
	srv.ln = ln
	go func() {
		s := <-sig
		srv.halt("合図 "+s.String(), false)
	}()

	log.Printf("待ち受け開始 socket=%s owner-uid=%d bws=%s 秘密の数=%d Pushover=%v", *sockPath, *ownerUID, bwsReal, len(allow.secrets), push != nil)
	for {
		c, err := ln.AcceptUnix()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				break
			}
			srv.halt("待ち受けが失敗した", false)
			srv.conns.Wait()
			audit.close(auditEntry{UID: uint32(os.Geteuid()), PID: os.Getpid(), Result: "待ち受けが失敗して止めた"})
			os.Remove(*sockPath)
			return err
		}
		srv.conns.Add(1)
		go func() {
			defer srv.conns.Done()
			srv.handle(c)
		}()
	}
	srv.shutdown(marker, *sockPath)
	return nil
}

// shutdown は、止め始めた後の片づけ。動いている接続が終わるのを待ち、停止の行を書き、
// monban stop で止めたなら止めた印を置き、ソケットを消して、知らせる。
func (s *server) shutdown(marker, sock string) {
	// 待っている要求は stop で断られ、取り出し中の bws は取り消される。動いている接続も締め切り（30秒）までに終わる。
	s.conns.Wait()
	seq, h, err := s.audit.close(auditEntry{UID: uint32(os.Geteuid()), PID: os.Getpid(), Result: "門番を止めた（" + s.why + "）"})
	if err != nil {
		log.Printf("停止の行を書けない: %v", err)
	}
	// 止めた印は、停止の行の後、ソケットを消す前に置く（monban stop が「止まった」と言うときには、印がある）。
	markNote := ""
	if s.mark && marker != "" {
		if err := writeMarker(marker, s.why, seq, h); err != nil {
			log.Printf("注意！！ 止めた印を置けない（再起動で動き出す）: %v", err)
			markNote = "\n注意：止めた印を置けなかった"
		} else {
			log.Printf("止めた印を置いた: %s（解除は sudo rm %s）", marker, marker)
			markNote = "\n止めた印を置いた"
		}
	}
	// ソケットは停止の行を書いてから消す。monban stop は、ソケットが消えたのを見て「止まった」と言う。
	os.Remove(sock)
	log.Printf("止めた（%s） ログ seq %d ハッシュ %s", s.why, seq, h)
	s.notify("monban 停止", fmt.Sprintf("門番を止めた（%s）\nログ seq %d\nハッシュ %s%s", s.why, seq, short(h), markNote))
}

// short は、知らせに載せるハッシュの頭（16文字）。
func short(h string) string {
	if len(h) > 16 {
		return h[:16]
	}
	return h
}

// refuseIfStopped は、止めた印があれば、ログに1行足して（連鎖の決まりどおり、停止の行として）閉じ、true を返す。
// 門番は「あるかないか」だけを見る（中身は人が読むための表示で、信じない）。
// 印があるときは正常に終わる（失敗で終わると、launchd の KeepAlive が起き直させ続ける）。
func refuseIfStopped(marker string, audit *auditLog, push *pushover) (bool, error) {
	_, err := os.Lstat(marker)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		audit.close(auditEntry{UID: uint32(os.Geteuid()), PID: os.Getpid(), Result: "止めた印を確かめられずに起動しなかった"})
		return true, fmt.Errorf("止めた印を確かめられない: %w", err)
	}
	seq, h, err := audit.close(auditEntry{UID: uint32(os.Geteuid()), PID: os.Getpid(), Result: "止めた印があるので起動しなかった"})
	if err != nil {
		log.Printf("ログに書けない: %v", err)
	}
	log.Printf("止めた印があるので起動しない: %s（ログ seq %d ハッシュ %s）", marker, seq, h)
	log.Printf("印の中身（表示だけ。門番は中身を信じない）:\n%s", markerText(marker))
	log.Printf("疑いが晴れたら、印を消してから起動する: sudo rm %s", marker)
	(&server{push: push}).notify("monban 起動しなかった", fmt.Sprintf("止めた印があるので起動しなかった\nログ seq %d\nハッシュ %s", seq, short(h)))
	return true, nil
}

// writeMarker は、止めた印を置く。中身は人が読むためのもの（門番は有無だけを見る）。
func writeMarker(path, why string, seq uint64, h string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(f, "門番を止めた印（monban stop）\n時刻 %s\n理由 %s\n停止の行 seq %d ハッシュ %s\n"+
		"この印がある間、門番は起動しない。疑いが晴れたら消す: sudo rm %s\n",
		time.Now().Format(time.RFC3339), why, seq, h, path)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// markerText は、印の中身を表示用に読む（1 KiB まで）。読めなければ、そう書く。
func markerText(path string) string {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return "（読めない: " + err.Error() + "）"
	}
	defer f.Close()
	b, _ := io.ReadAll(io.LimitReader(f, 1024))
	return strings.ToValidUTF8(string(b), "?")
}

// notify は、起動と停止の知らせ。Pushover がなければ何もしない。送れなくても画面のログに出すだけ。
func (s *server) notify(title, body string) {
	if s.push == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := s.push.note(ctx, title, body); err != nil {
		log.Printf("%s の知らせを送れない: %v", title, err)
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
	if req.Op == opKill {
		e.Op = opKill
		s.handleKill(c, e, p)
		return
	}
	// 止め始めた後に届いた要求（待ち受けを閉じる直前に繋いだもの）は、中身を見ずに断る。
	if s.stopped() {
		deny(outStopped)
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

	if s.stopped() {
		deny(outStopped)
		return
	}
	value, err := s.bws.get(s.ctx, req.Secret, id)
	if s.stopped() {
		// 取り出している間に止めた。取れていても渡さない。
		deny(outStopped)
		return
	}
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
	// 止める要求は、この行より前に来れば渡さない（上で見ている）。後に来たら、もう渡している。
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

// opKill は、monban stop の接続の op。門番が書く停止の行（op stop）とは分ける。
const opKill = "kill"

// handleKill は、monban stop の接続。番号は要らない（止める向きは、間違って押しても秘密は出ない）。
// 持ち主の uid の照合は handle で済んでいる。止める要求の行を書いてから「OK <ID>」を返し、止め始める。
// ログに書けなくても止める（止める向きは安全な側）。
func (s *server) handleKill(c net.Conn, e auditEntry, p peer) {
	e.Result = "止める要求を受けた"
	if err := s.audit.write(e); err != nil {
		log.Printf("ログに書けない（それでも止める）: %v", err)
	}
	log.Printf("止める要求 id=%s uid=%d pid=%d", e.ID, p.uid, p.pid)
	fmt.Fprintf(c, "OK %s\n", e.ID)
	s.halt(fmt.Sprintf("monban stop、uid %d pid %d", p.uid, p.pid), true)
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
