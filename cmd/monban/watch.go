package main

// 見張り（monban watch）：門番が止まったままなのに、人が気づかない、をなくす。
// 2026-11-17、macOS の大型アップデートで /var/log/monban が消え、門番は 3 日半起動できずにいた。
// launchd がログのファイルを開けずに失敗していたので、門番のコードまで届かず、Pushover も飛ばなかった。
// だから門番とは別の launchd の仕事（15 分ごと、_monban で動く）として、外から見る。
//
// - 門番が動いていない（launchd の state が running でない、またはソケットがない）なら、Pushover で知らせる
// - 止めた印があるときは、人が monban stop で止めたので知らせない
// - 知らせ続けて溢れないように、知らせたら state-dir に印（watch-alerted）を置き、6 時間は黙る。戻ったら1回だけ知らせて印を消す
// - 見張り自身は /var/log に何も書かない（ログの置き場が消えても動く）。launchd の plist にも stderr の置き場を書かない

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const (
	watchAlertName = "watch-alerted"
	watchRealert   = 6 * time.Hour
)

// launchdState は、launchctl print の出力から、仕事の一番上の段の state と last exit code を取り出す。
// 下の段（タブ2つ以上）にも state の行があるので、タブ1つの行だけを見る。
func launchdState(out string) (state, lastExit string) {
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "\t") || strings.HasPrefix(line, "\t\t") {
			continue
		}
		k, v, ok := strings.Cut(strings.TrimPrefix(line, "\t"), " = ")
		if !ok {
			continue
		}
		switch k {
		case "state":
			if state == "" {
				state = v
			}
		case "last exit code":
			if lastExit == "" {
				lastExit = v
			}
		}
	}
	return state, lastExit
}

// watchAction は、見たことから、することを決める。
//   - "down"：止まっていると知らせる（前に知らせていない、または realert より前に知らせた）
//   - "back"：戻ったと知らせる（前に止まっていると知らせていた）
//   - "clear"：知らせた印を黙って消す（人が monban stop で止めた）
//   - ""：何もしない
func watchAction(now time.Time, up, stopped bool, alerted bool, alertedAt time.Time, realert time.Duration) string {
	switch {
	case stopped:
		if alerted {
			return "clear"
		}
		return ""
	case up:
		if alerted {
			return "back"
		}
		return ""
	case !alerted || now.Sub(alertedAt) >= realert:
		return "down"
	}
	return ""
}

func runWatch(args []string) error {
	fl := flag.NewFlagSet("watch", flag.ContinueOnError)
	label := fl.String("label", "io.github.mediiiiium.monban", "見張る門番の launchd のラベル")
	sockPath := fl.String("socket", defaultSocket, "門番のソケットの置き場")
	stateDir := fl.String("state-dir", "", "門番の状態のディレクトリ（止めた印を見る・知らせた印を置く。必須）")
	pushFile := fl.String("pushover", "", "Pushover のファイル（必須）")
	if err := fl.Parse(args); err != nil {
		return err
	}
	if fl.NArg() != 0 || *stateDir == "" || *pushFile == "" {
		return errors.New("使い方: monban watch -state-dir <ディレクトリ> -pushover <ファイル> [-label <ラベル>] [-socket <パス>]")
	}
	if err := checkPrivate(*stateDir, 0o700, true); err != nil {
		return fmt.Errorf("状態のディレクトリ: %w", err)
	}
	push, err := readPushover(*pushFile)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	out, lerr := exec.CommandContext(ctx, "/bin/launchctl", "print", "system/"+*label).Output()
	cancel()
	state, lastExit := launchdState(string(out))
	if lerr != nil {
		state = "launchd に入っていない"
	}
	_, serr := os.Lstat(*sockPath)
	sock := serr == nil
	up := lerr == nil && state == "running" && sock

	_, merr := os.Lstat(filepath.Join(*stateDir, stoppedName))
	stopped := merr == nil

	alertPath := filepath.Join(*stateDir, watchAlertName)
	ast, aerr := os.Lstat(alertPath)
	alerted := aerr == nil
	var alertedAt time.Time
	if alerted {
		alertedAt = ast.ModTime()
	} else if !errors.Is(aerr, fs.ErrNotExist) {
		return fmt.Errorf("知らせた印を確かめられない: %w", aerr)
	}

	switch watchAction(time.Now(), up, stopped, alerted, alertedAt, watchRealert) {
	case "down":
		sockText := "ない"
		if sock {
			sockText = "ある"
		}
		body := fmt.Sprintf("門番が動いていない（秘密は渡らない側）\nlaunchd: %s / 最後の終了 %s\nソケット: %s\n調べる: sudo launchctl print system/%s\n次の知らせは %d 時間後（戻ったら知らせる）",
			nz(state), nz(lastExit), sockText, *label, int(watchRealert/time.Hour))
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := push.note(ctx, "monban 止まっている", body); err != nil {
			return fmt.Errorf("知らせを送れない: %w", err)
		}
		return touchAlert(alertPath)
	case "back":
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := push.note(ctx, "monban 戻った", "門番が動いている（launchd: running、ソケットあり）"); err != nil {
			return fmt.Errorf("知らせを送れない: %w", err)
		}
		return os.Remove(alertPath)
	case "clear":
		return os.Remove(alertPath)
	}
	return nil
}

// touchAlert は、知らせた印を置くか、時刻を今にする。シンボリックリンクはたどらない。
func touchAlert(path string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	now := time.Now()
	return os.Chtimes(path, now, now)
}

func nz(s string) string {
	if s == "" {
		return "（不明）"
	}
	return s
}
