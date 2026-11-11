// monban は、Bitwarden Secrets Manager の前に置く門番。
// 8段目まで：daemon は許可リストで照らし、承認が要る項目は Pushover で知らせて monban approve を待ち、
// bws で秘密を取り出してログを足し、値を run に渡す。run は値を環境変数に入れてコマンドを exec する。
// ログはハッシュの連鎖にし、verify-log で確かめる。stop は門番を止め、合言葉の失効の手順を出す。
package main

import (
	"fmt"
	"os"
)

const usage = `使い方: monban <サブコマンド> [引数]

サブコマンド:
  daemon                    門番を動かす（_monban で常駐する側）
  run <秘密> -- <コマンド> [引数…]
                            秘密を環境変数に入れてコマンドを実行する
  approve <番号>            承認を待っている要求を見て、承認する（番号は Pushover か門番のログにある8桁）
  deny <番号>               承認を待っている要求を拒否する
  stop                      門番を止め、合言葉の失効の手順を出す（キルスイッチ）
  verify-log [-at <seq>] <ログ>
                            ログのハッシュの連鎖を確かめる（-at はその seq の行のハッシュも出す）
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
	case "verify-log":
		err = runVerifyLog(args)
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
