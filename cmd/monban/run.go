package main

// run：門番に繋いで「どの秘密が欲しいか」を送り、返事を表示する。
// 4段目では、門番が許可リストで照らし、bws で取れたかどうかが返るだけ。値を受け取って exec するのは5段目（注入）。
// 5段目で守ること：値は引数にもファイルにも置かず、受け取ったらソケットを閉じてからすぐ exec する。

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	defer c.Close()
	c.SetDeadline(time.Now().Add(30 * time.Second))

	req, _ := json.Marshal(request{Agent: agentName(), Secret: secret, Command: command})
	if _, err := c.Write(append(req, '\n')); err != nil {
		return fmt.Errorf("門番に送れない: %w", err)
	}
	line, err := bufio.NewReader(c).ReadString('\n')
	if err != nil {
		return fmt.Errorf("門番の返事を読めない: %w", err)
	}
	line = strings.TrimSpace(line)
	if id, ok := strings.CutPrefix(line, "NG "); ok || line == "NG" {
		return fmt.Errorf("門番に断られた（リクエストの ID %s）。理由は門番のログを見てください", id)
	}
	rest, ok := strings.CutPrefix(line, "OK ")
	if !ok {
		return errors.New("門番の返事を読めない")
	}
	fmt.Println("門番に通された: " + rest)
	return nil
}
