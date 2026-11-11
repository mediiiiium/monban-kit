package main

// 8段目：キルスイッチ（monban stop）。
// 門番に「止めて」と送り、止まった（ソケットが消えた）のを見てから、合言葉を失効させる手順を出す。
// 門番を止めても、合言葉は生きていて、止める前に渡した値も取り戻せない。失効させても、合言葉でログイン済みの相手は、
// 誰であっても最大1時間は読める。
// だから「即時の遮断」ではなく「門番の停止＋失効の手続き」。

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"strings"
	"time"
)

// stopWait は、止まるのを待つ上限。動いている接続の締め切り（30秒）と、停止の行を書く分より長くする。
const stopWait = 45 * time.Second

const revokeSteps = `
門番を止めても、次のことは変わらない：
  - 合言葉（アクセストークン）は生きている
  - 止める前に渡した値は、取り戻せない（注入先のコマンドの中で生きている）

漏れの疑いがないとき（試しや、念のため止めたとき）：
  合言葉のファイルはそのまま使える。門番を起動し直せばよい。
  本番の形では、止めた印があるうちは起動しないので、印を消してから起動する
  （入れ方の既定の置き場なら sudo rm /var/db/monban/stopped）。

漏れの疑いがあるとき（合言葉のファイルが読まれたかもしれない）：
  1. Bitwarden のウェブの画面に入り、組織の Secrets Manager を開く
  2. 「Machine accounts」→ 門番のマシンアカウント →「Access tokens」タブ
  3. 合言葉のメニュー →「Revoke access token」
  4. 新しい合言葉を作り、合言葉のファイルを入れ替え、止めた印を消してから、門番を起動する
  失効させても、合言葉でログイン済みの相手は、誰であっても最大1時間は読み続けられる（即時の遮断ではない）。
  だから、Secrets Manager の中の秘密そのもの（API キーなど）も、それぞれのサービスで取り替える。
`

func runStop(args []string) error {
	if len(args) != 0 {
		return errors.New("使い方: monban stop")
	}
	err := stopDaemon(os.Stdout, socketPath(), stopWait)
	// 門番に繋げなくても、失効の手順は出す（門番が固まっていても、合言葉は止められる）。
	fmt.Print(revokeSteps)
	return err
}

// stopDaemon は、止める要求を送り、ソケットが消えるまで待つ。
func stopDaemon(w io.Writer, sock string, limit time.Duration) error {
	c, err := net.DialTimeout("unix", sock, 5*time.Second)
	if err != nil {
		return fmt.Errorf("門番に繋げない（もう止まっているか、置き場が違う）: %w", err)
	}
	c.SetDeadline(time.Now().Add(30 * time.Second))
	b, _ := json.Marshal(request{Op: opKill})
	_, err = c.Write(append(b, '\n'))
	var line string
	if err == nil {
		line, err = bufio.NewReader(io.LimitReader(c, 256)).ReadString('\n')
	}
	c.Close()
	if err != nil {
		return errors.New("門番の返事を読めない（固まっているなら、門番のプロセスを直接止める）")
	}
	line = strings.TrimSuffix(line, "\n")
	id, ok := strings.CutPrefix(line, "OK ")
	if !ok {
		return errors.New("門番に断られた（繋いできた uid が門番の持ち主と違う）")
	}
	fmt.Fprintf(w, "止める要求を受け付けた（リクエストの ID %s）。止まるのを待っています…\n", clip(id, 32))
	deadline := time.Now().Add(limit)
	for {
		if _, err := os.Lstat(sock); errors.Is(err, fs.ErrNotExist) {
			fmt.Fprintln(w, "門番は止まった（新しい要求は受けない。承認待ちの要求は断った。まだ渡していない値は渡していない）")
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%d秒待っても門番が止まらない（ソケットが残っている: %s）", int(limit/time.Second), sock)
		}
		time.Sleep(200 * time.Millisecond)
	}
}
