package main

// 2段目（ソケット）：run は門番に繋いで、通されたかどうかを表示するだけ。
// 秘密を受け取って exec するのは5段目（注入）。

import (
	"bufio"
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

func runRun(args []string) error {
	c, err := net.DialTimeout("unix", socketPath(), 5*time.Second)
	if err != nil {
		return fmt.Errorf("門番に繋げない: %w", err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))

	line, err := bufio.NewReader(c).ReadString('\n')
	if err != nil {
		return fmt.Errorf("門番の返事を読めない: %w", err)
	}
	line = strings.TrimSpace(line)
	if line != "OK" {
		return errors.New("門番に断られた: " + strings.TrimPrefix(line, "NG "))
	}
	fmt.Println("門番に通された（秘密の受け渡しはまだ作っていない）")
	return nil
}
