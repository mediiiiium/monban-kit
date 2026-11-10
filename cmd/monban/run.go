package main

// run：門番に繋いで「どの秘密が欲しいか」を送り、返事を表示する。
// 3段目では、門番が bws で取れたかどうかが返るだけ。値を受け取って exec するのは5段目（注入）。

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
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

func runRun(args []string) error {
	if len(args) < 1 || args[0] == "--" {
		return errors.New("使い方: monban run <秘密> -- <コマンド>")
	}
	secret := args[0]

	c, err := net.DialTimeout("unix", socketPath(), 5*time.Second)
	if err != nil {
		return fmt.Errorf("門番に繋げない: %w", err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(30 * time.Second))

	req, _ := json.Marshal(request{Agent: agentName(), Secret: secret})
	if _, err := c.Write(append(req, '\n')); err != nil {
		return fmt.Errorf("門番に送れない: %w", err)
	}
	line, err := bufio.NewReader(c).ReadString('\n')
	if err != nil {
		return fmt.Errorf("門番の返事を読めない: %w", err)
	}
	line = strings.TrimSpace(line)
	if line != "OK" && !strings.HasPrefix(line, "OK ") {
		return errors.New("門番に断られた: " + strings.TrimPrefix(line, "NG "))
	}
	fmt.Println("門番に通された: " + strings.TrimPrefix(line, "OK "))
	return nil
}
