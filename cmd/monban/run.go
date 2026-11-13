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
// 5段目では、ここで決めたパスをそのまま exec する（送ったものと違うものを動かさない）。
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
	// 値を受け取っても受け取らなくても、exec の前に閉じる。close-on-exec に頼らない。
	c.Close()
	if err != nil {
		return err
	}
	// 門番も短い値は渡さないが、偽の門番や古い門番に繋いだときのために、run の側でも確かめる。
	if len(value) < minValue {
		return fmt.Errorf("値が短すぎて出力から伏せられないので、動かさない（%dバイト未満）", minValue)
	}
	env := injectEnv(os.Environ(), secret, value)

	// 合図は、子が動く前から受けておく（取りこぼさない）。受けた合図は子に渡す。
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, forwardSignals...)
	res, err := runChild(path, command, env, value, os.Stdin, os.Stdout, os.Stderr, sigs)
	signal.Stop(sigs)
	if err != nil {
		return fmt.Errorf("コマンドを実行できない（%s）: %v", path, err)
	}
	if res.redacted > 0 {
		fmt.Fprintf(os.Stderr, "monban run: 出力から秘密の値を %d か所伏せた\n", res.redacted)
	}
	exitLike(res)
	return nil
}

// forwardSignals は、run が受けて子に渡す合図。
var forwardSignals = []os.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP}

// childResult は、子の終わり方と、伏せた数。
type childResult struct {
	code     int            // 終了コード（合図で死んだときは -1）
	signal   syscall.Signal // 子を殺した合図（合図で死んでいなければ 0）
	redacted int
}

// runChild は、path を子として動かし、stdout・stderr から値を伏せて流し、子の終わり方を返す。
// sigs に届いた合図は、子に渡す。子が孫を残して出力を握っていても、子が終わってから2秒で待つのをやめる。
func runChild(path string, argv, env []string, value string, stdin io.Reader, stdout, stderr io.Writer, sigs <-chan os.Signal) (childResult, error) {
	outR, errR := newRedactor(stdout, value), newRedactor(stderr, value)
	cmd := &exec.Cmd{Path: path, Args: argv, Env: env, Stdin: stdin, Stdout: outR, Stderr: errR, WaitDelay: 2 * time.Second}
	if err := cmd.Start(); err != nil {
		return childResult{}, err
	}
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
	err := cmd.Wait()
	close(done)
	outR.Flush()
	errR.Flush()
	res := childResult{code: -1, redacted: outR.n + errR.n}
	if cmd.ProcessState == nil {
		return res, err
	}
	// 終了コードが0でない・孫が出力を握っていた（ErrWaitDelay）は、子の終わり方として返す。エラーではない。
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
