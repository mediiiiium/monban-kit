package main

// 起動時に、門番が頼るファイルの持ち主とモードを調べる道具。
// 想定と違えば起動しない（fail closed）。

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

func statUID(st fs.FileInfo) uint32 { return st.Sys().(*syscall.Stat_t).Uid }

// checkPrivate は、path が自分（euid）の持ち物で、シンボリックリンクでなく、
// モードがちょうど want であることを確かめる。
func checkPrivate(path string, want fs.FileMode, wantDir bool) error {
	st, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if wantDir && !st.IsDir() {
		return fmt.Errorf("ディレクトリではない（シンボリックリンクも不可）: %s", path)
	}
	if !wantDir && !st.Mode().IsRegular() {
		return fmt.Errorf("普通のファイルではない（シンボリックリンクも不可）: %s", path)
	}
	if me := uint32(os.Geteuid()); statUID(st) != me {
		return fmt.Errorf("持ち主が自分（uid %d）ではない: uid %d: %s", me, statUID(st), path)
	}
	if st.Mode().Perm() != want {
		return fmt.Errorf("モードが %04o ではない: %04o: %s", want, st.Mode().Perm(), path)
	}
	return nil
}

// trustedPath は、path のシンボリックリンクをたどった本当の場所を返す。
// その場所と、上のディレクトリ全部が、root か自分の持ち物で、
// グループとほかの人から書き込めないことを確かめる。
// 書き込める人がいれば、その人は中身を差し替えられる（bws なら合言葉を盗める）。
func trustedPath(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("絶対パスにしてください: %s", path)
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	me := uint32(os.Geteuid())
	for p := real; ; p = filepath.Dir(p) {
		st, err := os.Lstat(p)
		if err != nil {
			return "", err
		}
		if u := statUID(st); u != 0 && u != me {
			return "", fmt.Errorf("root でも自分（uid %d）でもない人の持ち物: uid %d: %s", me, u, p)
		}
		// 上のディレクトリが sticky（/tmp など）なら、ほかの人は中のものを消せも替えもできないので許す。
		// path そのものには許さない。
		sticky := p != real && st.IsDir() && st.Mode()&fs.ModeSticky != 0
		if st.Mode().Perm()&0o022 != 0 && !sticky {
			return "", fmt.Errorf("グループかほかの人が書き込める: %04o: %s", st.Mode().Perm(), p)
		}
		if p == "/" {
			break
		}
	}
	return real, nil
}
