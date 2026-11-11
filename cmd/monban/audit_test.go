package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// writeSome は、新しいログに n 行書いて閉じ、ファイルの中身を返す。
func writeSome(t *testing.T, path string, n int) []byte {
	t.Helper()
	a, st, err := openAudit(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	if st.Lines != 0 && n > 0 {
		t.Fatalf("新しいはずのログに %d 行", st.Lines)
	}
	for i := 0; i < n; i++ {
		if err := a.write(auditEntry{Agent: "a\nb", Secret: "MONBAN_TEST", Result: "渡した"}); err != nil {
			t.Fatal(err)
		}
	}
	a.f.Close()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func verifyBytes(b []byte, at uint64) (chainState, string, error) {
	return verifyChain(bytes.NewReader(b), at)
}

func TestChain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	b := writeSome(t, path, 4)
	lines := bytes.Split(bytes.TrimSuffix(b, []byte("\n")), []byte("\n"))
	if len(lines) != 4 {
		t.Fatalf("行の数 %d", len(lines))
	}
	if !bytes.Contains(lines[0], []byte(`"seq":1,"prev":"`+zeroHash+`"`)) {
		t.Fatalf("最初の行: %s", lines[0])
	}
	st, h, err := verifyBytes(b, 2)
	if err != nil {
		t.Fatal(err)
	}
	if st.Lines != 4 || st.End != int64(len(b)) || st.Last != lineHash(lines[3]) || h != lineHash(lines[1]) {
		t.Fatalf("確かめた結果が違う: %+v %s", st, h)
	}
	if _, _, err := verifyBytes(b, 9); err == nil {
		t.Fatal("ない seq を通した")
	}

	join := func(ls ...[]byte) []byte { return append(bytes.Join(ls, []byte("\n")), '\n') }
	edited := bytes.Replace(lines[1], []byte("渡した"), []byte("断った"), 1)
	for name, c := range map[string]struct {
		b    []byte
		line uint64
	}{
		"途中を直した":    {join(lines[0], edited, lines[2], lines[3]), 3},
		"途中を消した":    {join(lines[0], lines[2], lines[3]), 2},
		"途中に入れた":    {join(lines[0], lines[1], lines[1], lines[2], lines[3]), 3},
		"入れ替えた":     {join(lines[0], lines[2], lines[1], lines[3]), 2},
		"連鎖の前の形の行":  {join([]byte(`{"time":"x","result":"渡した"}`)), 1},
		"JSON ではない": {join(lines[0], []byte("hello")), 2},
	} {
		_, _, err := verifyBytes(c.b, 0)
		var ce *chainError
		if !errors.As(err, &ce) || ce.Partial || ce.Line != c.line {
			t.Errorf("%s: %v（%d 行目のはず）", name, err, c.line)
		}
	}

	// 書きかけ：改行で終わっていない断片。完全な行は失われていないと言い、何バイト目までかを出す。
	_, _, err = verifyBytes(append(append([]byte{}, b...), `{"seq":5,"pr`...), 0)
	var ce *chainError
	if !errors.As(err, &ce) || !ce.Partial || ce.State.Lines != 4 || ce.State.End != int64(len(b)) {
		t.Fatalf("書きかけ: %v", err)
	}
	// 末尾の完全な行を消すと、連鎖だけではわからない（最後が停止の行かどうかで見る）。
	if st, _, err := verifyBytes(join(lines[0], lines[1]), 0); err != nil || st.Lines != 2 {
		t.Fatalf("末尾を消した: %v %+v", err, st)
	}
}

func TestOpenAudit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.log")
	writeSome(t, path, 2)

	// 開き直すと続きから書き、閉じると停止の行が最後になる。
	a, st, err := openAudit(path, 0)
	if err != nil || st.Lines != 2 || st.LastOp != "" {
		t.Fatalf("開き直し: %v %+v", err, st)
	}
	if err := a.write(auditEntry{Op: opStart, Result: "門番を起動した"}); err != nil {
		t.Fatal(err)
	}
	seq, _, err := a.close(auditEntry{Result: "門番を止めた"})
	if err != nil || seq != 4 {
		t.Fatalf("閉じた: %v seq %d", err, seq)
	}
	if err := a.write(auditEntry{Result: "渡した"}); err == nil {
		t.Fatal("閉じた後に書けた")
	}
	b, _ := os.ReadFile(path)
	st, _, err = verifyBytes(b, 0)
	if err != nil || st.Lines != 4 || st.LastOp != opStop {
		t.Fatalf("閉じた後: %v %+v", err, st)
	}

	// モード：0600 か、同じグループの 0640 だけ。
	fi, _ := os.Stat(path)
	gid := fi.Sys().(*syscall.Stat_t).Gid
	os.Chmod(path, 0o640)
	if a, _, err := openAudit(path, gid); err != nil {
		t.Fatalf("同じグループの 0640 を断った: %v", err)
	} else {
		a.f.Close()
	}
	if _, _, err := openAudit(path, gid+1); err == nil {
		t.Fatal("違うグループの 0640 を通した")
	}
	os.Chmod(path, 0o644)
	if _, _, err := openAudit(path, gid); err == nil {
		t.Fatal("0644 を通した")
	}
	os.Chmod(path, 0o600)

	// 連鎖が合わなければ開かない。書きかけなら、直し方を出す。
	os.WriteFile(path, append(b, `{"seq":5`...), 0o600)
	if _, _, err := openAudit(path, 0); err == nil || !strings.Contains(err.Error(), "書きかけ") || !strings.Contains(err.Error(), "取り除けば") {
		t.Fatalf("書きかけで開いた: %v", err)
	}
	os.WriteFile(path, bytes.Replace(b, []byte("渡した"), []byte("断った"), 1), 0o600)
	if _, _, err := openAudit(path, 0); err == nil || !strings.Contains(err.Error(), "2 行目") {
		t.Fatalf("書き換えたログで開いた: %v", err)
	}
}

func TestAuditWriteFailSticks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	a, _, err := openAudit(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	a.f.Close() // 書けなくする
	if err := a.write(auditEntry{Result: "x"}); err == nil {
		t.Fatal("閉じたファイルに書けた")
	}
	if err := a.write(auditEntry{Result: "x"}); err == nil || !strings.Contains(err.Error(), "書き損じた") {
		t.Fatalf("書き損じた後も書こうとした: %v", err)
	}
}
