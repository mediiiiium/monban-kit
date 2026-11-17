package main

// ログの先頭の控え：ログがファイルごと消えた・入れ替わったのに、黙って新しいログで動き出す、をなくす。
// 2026-11-14、macOS の大型アップデートで /var/log が作り直され、ログはファイルごと消えた。
// 門番は空のファイルを作って seq 1 から書き始めるので、連鎖を確かめても何も見つからない。
//
// - 状態のディレクトリ（/var/db/monban、_monban だけの 0700）に、ログの最後の seq とハッシュを置く（起動の行と停止の行の後）
// - 起動のとき、控えがあれば、ログにその seq の行があり、ハッシュが同じかを確かめる。違えば起動しない
// - 起動しないときは Pushover で1回だけ知らせて、正常に終わる（失敗で終わると、launchd の KeepAlive が起き直させ、知らせが溢れる）
// - 控えがないとき（初めて・この仕組みより前から動いていた）は、確かめずに動き、控えを置く
//
// 守るのは事故（置き場ごと消えた）まで。_monban か root になれる人は、ログと控えを両方書き換えられる。
// わざとログを新しくするときは、控えも消す（sudo rm /var/db/monban/log-head）。

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"syscall"
)

const logHeadName = "log-head"

type logHead struct {
	Seq  uint64 `json:"seq"`
	Hash string `json:"hash"`
}

// readLogHead は控えを読む。なければ ok=false。
// 自分の持ち物の普通のファイルで 0600 でなければ、エラー。
func readLogHead(path string) (h logHead, ok bool, err error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if errors.Is(err, fs.ErrNotExist) {
		return h, false, nil
	}
	if err != nil {
		return h, false, fmt.Errorf("ログの先頭の控え: %w", err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return h, false, err
	}
	if !fi.Mode().IsRegular() || statUID(fi) != uint32(os.Geteuid()) || fi.Mode().Perm() != 0o600 {
		return h, false, fmt.Errorf("ログの先頭の控えが、自分の持ち物の普通のファイルで 0600 ではない: %s", path)
	}
	b, err := io.ReadAll(io.LimitReader(f, 1024))
	if err != nil {
		return h, false, err
	}
	if err := json.Unmarshal(b, &h); err != nil || h.Seq == 0 || len(h.Hash) != 64 {
		return h, false, fmt.Errorf("ログの先頭の控えが読めない: %s", path)
	}
	return h, true, nil
}

// checkLogHead は、ログに控えの seq の行があり、そのハッシュが控えと同じかを確かめる。
// ログは作らない（O_CREATE なし）。
func checkLogHead(logPath string, h logHead) error {
	f, err := os.OpenFile(logPath, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("ログがない（控えでは seq %d まであった）: %s", h.Seq, logPath)
	}
	if err != nil {
		return fmt.Errorf("ログのファイル: %w", err)
	}
	defer f.Close()
	_, got, err := verifyChain(f, h.Seq)
	if err != nil {
		return fmt.Errorf("ログが控えと合わない（控えでは seq %d まであった）: %w", h.Seq, err)
	}
	if got != h.Hash {
		return fmt.Errorf("ログの seq %d のハッシュが控えと違う（入れ替わったか、書き換えられた）: %s", h.Seq, short(got))
	}
	return nil
}

// writeLogHead は控えを置き換える。書きかけが残らないよう、一時ファイルに書いて fsync してから rename する。
func writeLogHead(path string, seq uint64, hash string) error {
	b, err := json.Marshal(logHead{Seq: seq, Hash: hash})
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	os.Remove(tmp) // 前に落ちたときの残り
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(append(b, '\n'))
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}
