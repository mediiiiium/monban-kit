package main

// run：門番に繋いで「どの秘密を・どのコマンドに」を送り、値を受け取って、そのコマンドを exec する（5段目：注入）。
// 守ること：
// - 値は引数にもファイルにも置かず、環境変数（秘密の名前）にだけ入れる。エラーにも出さない
// - 受け取ったら、門番へのソケットを閉じてからすぐ exec する（注入先に接続を引き継がない）
// - 門番に送ったコマンドの絶対パスを、そのまま exec する（送ったものと違うものを動かさない）

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
	env := injectEnv(os.Environ(), secret, value)
	// exec が成功すれば戻らない。run のメモリ（値を含む）は、ここで新しいコマンドに置き換わる。
	err = syscall.Exec(path, command, env)
	return fmt.Errorf("コマンドを実行できない（%s）: %v", path, err)
}

// ask は、要求を1行送り、返事の1行を読んで値を返す。エラーには値を入れない。
func ask(c net.Conn, req request) (string, error) {
	c.SetDeadline(time.Now().Add(30 * time.Second))
	b, _ := json.Marshal(req)
	if _, err := c.Write(append(b, '\n')); err != nil {
		return "", fmt.Errorf("門番に送れない: %w", err)
	}
	return readReply(c)
}

// readReply は、門番の返事（「OK <ID> <base64 の値>」か「NG <ID>」）を読む。
// 読む量は、値の上限から決めて絞る（偽の門番に大量に送られても膨らまない）。
func readReply(r io.Reader) (string, error) {
	limit := int64(base64.StdEncoding.EncodedLen(maxValue) + 128)
	line, err := bufio.NewReader(io.LimitReader(r, limit)).ReadString('\n')
	if err != nil {
		return "", errors.New("門番の返事を読めない")
	}
	line = strings.TrimSuffix(line, "\n")
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
