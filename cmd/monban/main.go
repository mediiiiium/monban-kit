// monban は、Bitwarden Secrets Manager の前に置く門番。
// 5段目まで：daemon は許可リストで照らし、bws で秘密を取り出してログを足し、値を run に渡す。run は値を環境変数に入れてコマンドを exec する。ほかの入口はまだ空。
package main

import (
	"errors"
	"fmt"
	"os"
)

var errNotYet = errors.New("まだ作っていない")

const usage = `使い方: monban <サブコマンド> [引数]

サブコマンド:
  daemon                    門番を動かす（_monban で常駐する側）
  run <秘密> -- <コマンド> [引数…]
                            秘密を環境変数に入れてコマンドを実行する
  approve <ID>              リクエストを承認する
  deny <ID>                 リクエストを拒否する
  stop                      門番を止める（キルスイッチ）
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "daemon":
		err = runDaemon(args)
	case "run":
		err = runRun(args)
	case "approve":
		err = runApprove(args)
	case "deny":
		err = runDeny(args)
	case "stop":
		err = runStop(args)
	case "-h", "--help", "help":
		fmt.Print(usage)
		return
	default:
		fmt.Fprintf(os.Stderr, "monban: 知らないサブコマンド %q\n\n%s", cmd, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "monban %s: %v\n", cmd, err)
		os.Exit(1)
	}
}

func runApprove(args []string) error { return errNotYet }
func runDeny(args []string) error    { return errNotYet }
func runStop(args []string) error    { return errNotYet }
