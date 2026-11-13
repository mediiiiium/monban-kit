package main

// run：門番に繋いで「どの秘密を・どのコマンドに」を送り、値を受け取って、そのコマンドを動かす（5段目：注入）。
// v1.5 からは exec せず、親として残ってコマンドを子として動かし、子の stdout・stderr から値を伏せて流す（redact.go）。
// 守ること：
// - 値は引数にもファイルにも置かず、環境変数（秘密の名前）にだけ入れる。エラーにも出さない
// - 受け取ったら、門番へのソケットを閉じてから子を動かす（子に接続を引き継がない）
// - 門番に送ったコマンドの絶対パスを、そのまま動かす（送ったものと違うものを動かさない）
// - 子の終わり方をそのまま返す。終了コードはそのまま。子が合図で死んだら、run も同じ合図で死ぬ（exec の形と同じに見える）
// - 値は子が終わるまで run のメモリに残る（exec の形では、exec の瞬間に消えていた）

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// socketPath は、環境変数 MONBAN_SOCKET があればそれを、なければ本番の置き場を使う。
// 同じ uid のエージェントはこれを偽の門番に向けられるが、偽物は秘密を持っていない。
func socketPath() string {
	if p := os.Getenv("MONBAN_SOCKET"); p != "" {
		return p
	}
	return defaultSocket
}

// agentName は、エージェントの名札（環境変数 MONBAN_AGENT）。認証ではない。
func agentName() string {
	if a := os.Getenv("MONBAN_AGENT"); a != "" {
		return a
	}
	return "名乗らず"
}

// resolveCommand は、コマンドを PATH から引いて絶対パスにする。門番は PATH を信じないので、run の側で決める。
// ここで決めたパスをそのまま子として動かす（送ったものと違うものを動かさない）。
func resolveCommand(name string) (string, error) {
	p, err := exec.LookPath(name)
	if err != nil {
		return "", err
	}
	return filepath.Abs(p)
}

func runRun(args []string) error {
	const use = "使い方: monban run <秘密> -- <コマンド> [引数…]"
	if len(args) < 3 || args[1] != "--" {
		return errors.New(use)
	}
	secret, command := args[0], args[2:]
	path, err := resolveCommand(command[0])
	if err != nil {
		return fmt.Errorf("コマンドが見つからない: %w", err)
	}
	command = append([]string{path}, command[1:]...)

	c, err := net.DialTimeout("unix", socketPath(), 5*time.Second)
	if err != nil {
		return fmt.Errorf("門番に繋げない: %w", err)
	}
	value, err := ask(c, request{Agent: agentName(), Secret: secret, Command: command})
	// 値を受け取っても受け取らなくても、子を動かす前に閉じる。close-on-exec に頼らない。
	c.Close()
	if err != nil {
		return err
	}
	// 門番も伏せられない値は渡さないが、偽の門番や古い門番に繋いだときのために、run の側でも確かめる。
	if err := checkRedactable(value); err != nil {
		return fmt.Errorf("%v。動かさない", err)
	}
	env := injectEnv(os.Environ(), secret, value)

	// 合図は、子が動く前から受けておく（取りこぼさない）。
	sigs, release := catchSignals()
	res, err := runChild(path, command, env, value, os.Stdin, os.Stdout, os.Stderr, sigs, release)
	release()
	if err != nil {
		return fmt.Errorf("コマンドを実行できない（%s）: %v", path, err)
	}
	if res.redacted > 0 {
		fmt.Fprintf(os.Stderr, "%s出力から秘密の値を %d か所伏せた\n", runNoticePrefix, res.redacted)
	}
	if res.copyErr {
		fmt.Fprintln(os.Stderr, runNoticePrefix+"子の出力を最後まで流せなかった")
	}
	if res.idleStop {
		fmt.Fprintf(os.Stderr, "%s子が終わった後、%d秒出力が来なかったので読むのをやめた（孫が出力を握っていた）\n", runNoticePrefix, int(grandchildWait/time.Second))
	}
	exitLike(res)
	return nil
}

// forwardSignals は、run が受けて子に渡す合図。pid を名指しして送られることが多いもの。
// swallowSignals は、run が受けて捨てる合図。端末の Ctrl-C・Ctrl-\ は、同じプロセスグループの子にも
// 端末から直接届くので、渡すと2回になる（2回目で後始末を飛ばすコマンドがある）。
// そのかわり、run の pid だけに送られた SIGINT・SIGQUIT は子に届かない。
var (
	forwardSignals = []os.Signal{syscall.SIGTERM, syscall.SIGHUP}
	swallowSignals = []os.Signal{syscall.SIGINT, syscall.SIGQUIT}
)

// catchSignals は、合図を受け始め、渡す合図の通り道と、受けるのをやめる関数を返す。
// 起動時に無視（nohup や、非対話のシェルの &）されていた合図は受けない。受けると Go が扱いを付け直し、
// 子が「無視」を引き継がなくなる（exec の形では引き継いでいた）。
// やめた後は、合図の扱いが既定に戻る（SIGTERM などで run が死ねる）。
func catchSignals() (<-chan os.Signal, func()) {
	fwd, swallow := make(chan os.Signal, 4), make(chan os.Signal, 4)
	for _, s := range forwardSignals {
		if !signal.Ignored(s) {
			signal.Notify(fwd, s)
		}
	}
	for _, s := range swallowSignals {
		if !signal.Ignored(s) {
			signal.Notify(swallow, s)
		}
	}
	var once sync.Once
	return fwd, func() {
		once.Do(func() {
			signal.Stop(fwd)
			signal.Stop(swallow)
		})
	}
}

// childResult は、子の終わり方と、伏せた数。
type childResult struct {
	code     int            // 終了コード（合図で死んだときは -1）
	signal   syscall.Signal // 子を殺した合図（合図で死んでいなければ 0）
	redacted int
	copyErr  bool // 子の出力を最後まで流せなかった（下流への書き込みの失敗）
	idleStop bool // 子が終わった後、grandchildWait のあいだ出力が来なかったので読むのをやめた
}

// grandchildWait は、子が終わった後、出力が来ないまま待つ時間。読めている間は待ち続ける
// （子が書いてパイプに残った分を、下流が遅くても捨てない）。来ないまま過ぎたら、孫が握っているとみてやめる。
const grandchildWait = 2 * time.Second

// idleReader は、子が終わった後（exited）は、読む前ごとに締め切りを grandchildWait 先に付け直す。
type idleReader struct {
	f      *os.File
	exited *atomic.Bool
}

func (r idleReader) Read(p []byte) (int, error) {
	if r.exited.Load() {
		r.f.SetReadDeadline(time.Now().Add(grandchildWait))
	}
	return r.f.Read(p)
}

// runChild は、path を子として動かし、stdout・stderr から値を伏せて流し、子の終わり方を返す。
// sigs に届いた合図は、子に渡す。子が終わったら release を呼ぶ（以後の合図は run 自身に効く。
// 下流への書き込みが詰まっても、run を止められるように）。
// パイプは自分で持つ。exec.Cmd に任せると、Wait が下流への書き込みの終わりまで待つため。
// 子が終わった後は、grandchildWait のあいだ出力が来なければ読むのをやめる（孫が握っているとき）。
// stdin は *os.File か nil（/dev/null）に限る。ほかの Reader だと exec.Cmd がコピーの goroutine を作り、
// Wait がその終わりを待つので、子が終わっても戻らないことがある。
func runChild(path string, argv, env []string, value string, stdin *os.File, stdout, stderr io.Writer, sigs <-chan os.Signal, release func()) (childResult, error) {
	outRed, errRed := newRedactor(stdout, value), newRedactor(stderr, value)
	outR, outW, err := os.Pipe()
	if err != nil {
		return childResult{}, err
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		outR.Close()
		outW.Close()
		return childResult{}, err
	}
	cmd := &exec.Cmd{Path: path, Args: argv, Env: env, Stdout: outW, Stderr: errW}
	if stdin != nil {
		// nil の *os.File を io.Reader に入れると nil でなくなるので、nil のときは入れない（/dev/null になる）。
		cmd.Stdin = stdin
	}
	err = cmd.Start()
	// 書く側は子だけが持つ。親が持ったままだと、子と孫が終わっても読み終わらない。
	outW.Close()
	errW.Close()
	if err != nil {
		outR.Close()
		errR.Close()
		return childResult{}, err
	}
	var wg sync.WaitGroup
	var copyErr, idleStop, exited atomic.Bool
	pump := func(r *os.File, red *redactor) {
		defer wg.Done()
		_, err := io.Copy(red, idleReader{r, &exited})
		if errors.Is(err, os.ErrDeadlineExceeded) {
			idleStop.Store(true)
			err = nil
		}
		// 手元に残した分は値の先頭の一部だけで、値の全体ではないので、止まったときも流す。
		if ferr := red.Flush(); err == nil {
			err = ferr
		}
		if err != nil {
			copyErr.Store(true)
		}
		r.Close() // 子か孫がまだ書いていれば、SIGPIPE か EPIPE になる
	}
	wg.Add(2)
	go pump(outR, outRed)
	go pump(errR, errRed)

	done := make(chan struct{})
	go func() {
		for {
			select {
			case s := <-sigs:
				cmd.Process.Signal(s)
			case <-done:
				return
			}
		}
	}()
	werr := cmd.Wait()
	close(done)
	if release != nil {
		release()
	}
	// 子が終わった後は、読む前ごとに締め切りを付け直す。いま読みかけの Read にも締め切りを付ける。
	exited.Store(true)
	deadline := time.Now().Add(grandchildWait)
	outR.SetReadDeadline(deadline)
	errR.SetReadDeadline(deadline)
	wg.Wait()

	res := childResult{code: -1, redacted: outRed.n + errRed.n, copyErr: copyErr.Load(), idleStop: idleStop.Load()}
	if cmd.ProcessState == nil {
		return res, werr
	}
	// 終了コードが0でないのは、子の終わり方として返す。エラーではない。
	if ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		res.signal = ws.Signal()
	} else {
		res.code = cmd.ProcessState.ExitCode()
	}
	return res, nil
}

// exitLike は、子と同じ終わり方で run を終える。
// 子が合図で死んだら、run も同じ合図で死ぬ。Go が既定で「その合図で死ぬ」扱いをするのは
// SIGINT・SIGTERM・SIGHUP と、受けられない SIGKILL だけなので、それ以外の合図は 128＋番号で終える。
func exitLike(res childResult) {
	switch res.signal {
	case 0:
		os.Exit(res.code)
	case syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGKILL:
		signal.Reset(res.signal)
		syscall.Kill(os.Getpid(), res.signal)
		time.Sleep(time.Second) // 合図が届くまで
	}
	os.Exit(128 + int(res.signal))
}

// ask は、要求を1行送り、返事を読んで値を返す。エラーには値を入れない。
// 承認を待つ間（6段目）は返事が遅れるので、締め切りは待ちの上限より長くする。
func ask(c net.Conn, req request) (string, error) {
	c.SetDeadline(time.Now().Add(approvalTimeout + time.Minute))
	b, _ := json.Marshal(req)
	if _, err := c.Write(append(b, '\n')); err != nil {
		return "", fmt.Errorf("門番に送れない: %w", err)
	}
	return readReply(c)
}

// readReply は、門番の返事（「OK <ID> <base64 の値>」か「NG <ID>」）を読む。
// その前に「WAIT <ID>」が1行来たら、承認を待っていると画面に出して、続きを待つ。
// 読む量は、値の上限から決めて絞る（偽の門番に大量に送られても膨らまない）。
func readReply(r io.Reader) (string, error) {
	limit := int64(base64.StdEncoding.EncodedLen(maxValue) + 256)
	br := bufio.NewReader(io.LimitReader(r, limit))
	line, err := br.ReadString('\n')
	if err != nil {
		return "", errors.New("門番の返事を読めない")
	}
	line = strings.TrimSuffix(line, "\n")
	if id, ok := strings.CutPrefix(line, "WAIT "); ok {
		fmt.Fprintf(os.Stderr, "monban run: 門番が人の承認を待っています（リクエストの ID %s、%d分で時間切れ）\n", clip(id, 32), int(approvalTimeout/time.Minute))
		if line, err = br.ReadString('\n'); err != nil {
			return "", errors.New("門番の返事を読めない")
		}
		line = strings.TrimSuffix(line, "\n")
	}
	if id, ok := strings.CutPrefix(line, "NG "); ok || line == "NG" {
		return "", fmt.Errorf("門番に断られた（リクエストの ID %s）。理由は門番のログを見てください", id)
	}
	rest, ok := strings.CutPrefix(line, "OK ")
	if !ok {
		return "", errors.New("門番の返事を読めない")
	}
	_, enc, ok := strings.Cut(rest, " ")
	if !ok {
		return "", errors.New("門番の返事を読めない")
	}
	v, err := base64.StdEncoding.Strict().DecodeString(enc)
	if err != nil || len(v) > maxValue || strings.IndexByte(string(v), 0) >= 0 {
		return "", errors.New("門番の返事の値を読めない")
	}
	return string(v), nil
}

// injectEnv は、環境変数の並びから同じ名前のものを除き、name=value を足す。
// 同じ名前が2つあると、コマンドによってどちらを読むかが変わるので、必ず1つにする。
func injectEnv(env []string, name, value string) []string {
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if k, _, _ := strings.Cut(kv, "="); k == name {
			continue
		}
		out = append(out, kv)
	}
	return append(out, name+"="+value)
}
