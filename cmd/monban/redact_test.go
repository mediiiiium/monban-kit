package main

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"math/rand"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// 1バイトずつ・まとめて、のどちらで書いても、strings.ReplaceAll と同じになる。
func TestRedactorSplits(t *testing.T) {
	cases := []struct{ value, in string }{
		{"dummy-value", "前 dummy-value 後\n"},
		{"dummy-value", "dummy-valuedummy-value"},
		{"dummy-value", "dummy-valu"},             // 値にならなかった先頭は、Flush で流れる
		{"dummy-value", "dummy-valu dummy-value"}, // 途中で外れてから一致
		{"aaaaaaab", "aaaaaaaaab"},                // 外れても、残りが先頭と一致し続ける
		{"abababac", "abababababac"},
		{"改行\nを含む値です", "x改行\nを含む値ですy"},
		{"dummy-value", "dum\x1b[31mmy-value"}, // 間に制御文字が挟まると一致しない
		{"dummy-value", ""},
	}
	for _, c := range cases {
		want := strings.ReplaceAll(c.in, c.value, redactMark)
		for _, step := range []int{1, 2, 3, len(c.in) + 1} {
			var out bytes.Buffer
			r := newRedactor(&out, c.value)
			for i := 0; i < len(c.in); i += step {
				r.Write([]byte(c.in[i:min(i+step, len(c.in))]))
			}
			r.Flush()
			if out.String() != want {
				t.Errorf("値 %q 入力 %q 刻み %d: %q（欲しいのは %q）", c.value, c.in, step, out.String(), want)
			}
			if got := strings.Count(c.in, c.value); r.n != got {
				t.Errorf("値 %q 入力 %q: 伏せた数 %d（欲しいのは %d）", c.value, c.in, r.n, got)
			}
		}
	}
}

// 2文字の並びで、ランダムな値・入力・刻みを試す（先頭と末尾が重なる値で、取りこぼしや出しすぎがないか）。
func TestRedactorRandom(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	gen := func(n int) string {
		b := make([]byte, n)
		for i := range b {
			b[i] = "ab"[rng.Intn(2)]
		}
		return string(b)
	}
	for i := 0; i < 20000; i++ {
		value, in := gen(minValue+rng.Intn(4)), gen(rng.Intn(60))
		var out bytes.Buffer
		r := newRedactor(&out, value)
		for rest := in; rest != ""; {
			k := 1 + rng.Intn(len(rest))
			r.Write([]byte(rest[:k]))
			rest = rest[k:]
		}
		r.Flush()
		if want := strings.ReplaceAll(in, value, redactMark); out.String() != want {
			t.Fatalf("値 %q 入力 %q: %q（欲しいのは %q）", value, in, out.String(), want)
		}
	}
}

// 値の先頭と一致していない分は、すぐ流す（Flush を待たない）。
func TestRedactorHoldsOnlyPrefix(t *testing.T) {
	var out bytes.Buffer
	r := newRedactor(&out, "dummy-value")
	r.Write([]byte("hello\ndum"))
	if out.String() != "hello\n" {
		t.Fatalf("流した分: %q", out.String())
	}
	r.Write([]byte("b\n"))
	if out.String() != "hello\ndumb\n" {
		t.Fatalf("外れた後: %q", out.String())
	}
}

// 伏せられない値：短すぎる（バイトで数える）・伏せた印と重なる。
func TestCheckRedactable(t *testing.T) {
	for _, c := range []struct {
		value string
		ok    bool
	}{
		{"1234567", false}, {"12345678", true},
		{"秘密", false}, {"秘密です", true}, // 6バイトと12バイト
		{"a" + redactMark + "b", false}, // 印を含む
		{"monban: 伏せ", false},           // 印に含まれる
		{"】" + "abcdefgh", false},       // 先頭が印の末尾と重なる
		{"abcdefgh" + "【mon", false},    // 末尾が印の先頭と重なる
		{"abcdefgh" + "【", false},       // 末尾が run の知らせの行の先頭と重なる
		{"]abcdefgh[", true},            // ASCII の括弧は重ならない
		{"abcdefgh【x", true},            // 印の先頭と同じ文字があっても、端で重ならなければよい
		{"dummy-value-for-test", true},
	} {
		if err := checkRedactable(c.value); (err == nil) != c.ok {
			t.Errorf("%q: %v", c.value, err)
		}
	}
}

// 伏せた印と重なる値を断る理由：断らないと、伏せた結果が値そのものになる。
func TestRedactMarkOverlapWouldLeak(t *testing.T) {
	v := "a" + redactMark + "b"
	var out bytes.Buffer
	r := newRedactor(&out, v)
	r.Write([]byte("a" + v + "b"))
	r.Flush()
	if !strings.Contains(out.String(), v) {
		t.Skip("重なっても漏れなかった（この試験は、checkRedactable が要る理由を示すためのもの）")
	}
	if checkRedactable(v) == nil {
		t.Fatal("漏れる値を通した")
	}
}

type failWriter struct{ calls int }

func (w *failWriter) Write(p []byte) (int, error) { w.calls++; return 0, errors.New("書けない") }

// 下流への書き込みが失敗したら、以後は流さずにエラーを返し続ける。
func TestRedactorWriteError(t *testing.T) {
	w := &failWriter{}
	r := newRedactor(w, "dummy-value")
	if _, err := r.Write([]byte("hello")); err == nil {
		t.Fatal("エラーにならない")
	}
	if n, err := r.Write([]byte("more dummy")); n != 0 || err == nil {
		t.Fatalf("2回目: %d %v", n, err)
	}
	if err := r.Flush(); err == nil {
		t.Fatal("Flush がエラーにならない")
	}
	if w.calls != 1 {
		t.Fatalf("失敗の後も下流に書いた: %d 回", w.calls)
	}
}

const testValue = "dummy-value-for-test"

func childOf(t *testing.T, script string) (childResult, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	env := injectEnv(os.Environ(), "MONBAN_TEST", testValue)
	res, err := runChild("/bin/sh", []string{"/bin/sh", "-c", script}, env, testValue, nil, &stdout, &stderr, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return res, stdout.String(), stderr.String()
}

// 子が値を出しても、stdout にも stderr にも値は出ない。終了コードはそのまま返る。
func TestRunChildRedacts(t *testing.T) {
	res, out, errOut := childOf(t, `printf '%s\n' "$MONBAN_TEST"; printf 'err:%s' "$MONBAN_TEST" >&2; exit 3`)
	if strings.Contains(out+errOut, testValue) {
		t.Fatalf("値が出た: %q %q", out, errOut)
	}
	if out != redactMark+"\n" || errOut != "err:"+redactMark {
		t.Fatalf("出力: %q %q", out, errOut)
	}
	if res.code != 3 || res.signal != 0 || res.redacted != 2 || res.copyErr || res.idleStop {
		t.Fatalf("終わり方: %+v", res)
	}
}

func TestRunChildSignal(t *testing.T) {
	res, _, _ := childOf(t, `kill -TERM $$`)
	if res.signal != syscall.SIGTERM || res.code != -1 {
		t.Fatalf("終わり方: %+v", res)
	}
}

// 子が孫を残して出力を握っていても、子が終わってから grandchildWait ほどで戻る。
// 合図の扱いを戻す release は、孫を待つ前（子が終わった直後）に呼ぶ。
func TestRunChildGrandchildHoldsPipe(t *testing.T) {
	var stdout, stderr bytes.Buffer
	t0 := time.Now()
	var released time.Duration
	res, err := runChild("/bin/sh", []string{"/bin/sh", "-c", `(sleep 4; echo late) & echo now`}, os.Environ(), testValue,
		nil, &stdout, &stderr, nil, func() { released = time.Since(t0) })
	took := time.Since(t0)
	if err != nil || res.code != 0 || stdout.String() != "now\n" || res.copyErr || !res.idleStop {
		t.Fatalf("終わり方: %+v %v 出力 %q", res, err, stdout.String())
	}
	if took < grandchildWait-500*time.Millisecond || took > grandchildWait+time.Second {
		t.Fatalf("戻るまで %v", took)
	}
	if released > time.Second {
		t.Fatalf("release が遅い: %v（孫を待ってから呼んだ）", released)
	}
}

// slowWriter は、最初の書き込みだけ grandchildWait より長くかかる下流。
type slowWriter struct {
	mu    sync.Mutex
	buf   bytes.Buffer
	calls int
}

func (w *slowWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.calls++; w.calls == 1 {
		time.Sleep(grandchildWait + 500*time.Millisecond)
	}
	return w.buf.Write(p)
}

// 子が終わった時点でパイプに残っている子の出力を、下流が遅くても捨てない。
// 子は "a" を書き、少し後に "b" を書いて終わる。run が "a" を下流に流している間に子が終わり、"b" はパイプに残る。
// 締め切りが「子が終わってから2秒」に固定だと、"a" を流し終えた時点で過ぎていて、"b" を読まずにやめてしまう。
func TestRunChildSlowDownstream(t *testing.T) {
	w := &slowWriter{}
	var stderr bytes.Buffer
	res, err := runChild("/bin/sh", []string{"/bin/sh", "-c", `printf a; sleep 0.2; printf b`}, os.Environ(), testValue,
		nil, w, &stderr, nil, nil)
	if err != nil || res.code != 0 || res.copyErr || res.idleStop {
		t.Fatalf("終わり方: %+v %v", res, err)
	}
	if got := w.buf.String(); got != "ab" {
		t.Fatalf("受け取ったのは %q", got)
	}
}

// 下流への書き込みが失敗したら、終わり方に印が付く。
func TestRunChildCopyError(t *testing.T) {
	var stderr bytes.Buffer
	res, err := runChild("/bin/sh", []string{"/bin/sh", "-c", "echo hi"}, os.Environ(), testValue, nil, &failWriter{}, &stderr, nil, nil)
	if err != nil || !res.copyErr {
		t.Fatalf("終わり方: %+v %v", res, err)
	}
}

// 子に渡す合図：run が受けた合図は子に届く。
func TestRunChildForwardsSignal(t *testing.T) {
	sigs := make(chan os.Signal, 1)
	sigs <- syscall.SIGHUP
	var stdout, stderr bytes.Buffer
	res, err := runChild("/bin/sh", []string{"/bin/sh", "-c", "sleep 5"}, os.Environ(), testValue, nil, &stdout, &stderr, sigs, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.signal != syscall.SIGHUP {
		t.Fatalf("終わり方: %+v", res)
	}
}

// run の側でも、伏せられない値では子を動かさない（偽の門番が短い値を返す）。
func TestRunRefusesShortValue(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "mbr")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "s")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		bufio.NewReader(c).ReadString('\n')
		io.WriteString(c, okReply("abc", "short"))
		c.Close()
	}()
	t.Setenv("MONBAN_SOCKET", sock)
	marker := filepath.Join(dir, "ran")
	err = runRun([]string{"MONBAN_TEST", "--", "/usr/bin/touch", marker})
	if err == nil || !strings.Contains(err.Error(), "短すぎ") {
		t.Fatalf("エラー: %v", err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("子が動いた")
	}
}

// 合図まわりは、このテストのバイナリを子として動かして確かめる（exitLike はプロセスを終え、
// 合図の扱いはプロセス全体に効くため）。
func helperMode(t *testing.T) bool {
	t.Helper()
	switch os.Getenv("MONBAN_HELPER") {
	case "":
		return false
	case "term":
		exitLike(childResult{code: -1, signal: syscall.SIGTERM})
	case "int":
		exitLike(childResult{code: -1, signal: syscall.SIGINT})
	case "hup":
		exitLike(childResult{code: -1, signal: syscall.SIGHUP})
	case "usr1":
		exitLike(childResult{code: -1, signal: syscall.SIGUSR1})
	case "code":
		exitLike(childResult{code: 7})
	case "ignored":
		// nohup の下で動く。SIGHUP は無視されているので受けず、子も無視を引き継ぐ。
		sigs, release := catchSignals()
		res, _ := runChild("/bin/sh", []string{"/bin/sh", "-c", `kill -HUP $$; echo survived`}, os.Environ(), testValue,
			nil, os.Stdout, os.Stderr, sigs, release)
		os.Exit(res.code)
	case "swallow":
		// 自分に SIGINT が来ても、run は死なず、子にも渡さない。
		sigs, release := catchSignals()
		go func() {
			time.Sleep(300 * time.Millisecond)
			syscall.Kill(os.Getpid(), syscall.SIGINT)
		}()
		res, _ := runChild("/bin/sh", []string{"/bin/sh", "-c", `sleep 1; echo done`}, os.Environ(), testValue,
			nil, os.Stdout, os.Stderr, sigs, release)
		os.Exit(res.code)
	}
	os.Exit(99)
	return true
}

func helper(t *testing.T, mode string, wrap ...string) (*exec.Cmd, string) {
	args := append(wrap, os.Args[0], "-test.run=^TestSignals$")
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Env = append(os.Environ(), "MONBAN_HELPER="+mode)
	out, _ := cmd.Output()
	return cmd, string(out)
}

func TestSignals(t *testing.T) {
	if helperMode(t) {
		return
	}
	// run 自身の終わり方：子が SIGINT・SIGTERM・SIGHUP で死んだら run も同じ合図で死に、それ以外は 128＋番号。
	for _, c := range []struct {
		mode string
		sig  syscall.Signal
		code int
	}{
		{"term", syscall.SIGTERM, -1}, {"int", syscall.SIGINT, -1}, {"hup", syscall.SIGHUP, -1},
		{"usr1", 0, 128 + int(syscall.SIGUSR1)}, {"code", 0, 7},
	} {
		cmd, _ := helper(t, c.mode)
		ws := cmd.ProcessState.Sys().(syscall.WaitStatus)
		if c.sig != 0 {
			if !ws.Signaled() || ws.Signal() != c.sig {
				t.Errorf("%s: 合図 %v で死んでいない（%v）", c.mode, c.sig, cmd.ProcessState)
			}
		} else if ws.Signaled() || ws.ExitStatus() != c.code {
			t.Errorf("%s: 終了コード %d ではない（%v）", c.mode, c.code, cmd.ProcessState)
		}
	}
	// 起動時に無視されていた合図は、子も無視を引き継ぐ。
	if cmd, out := helper(t, "ignored", "/usr/bin/nohup"); !strings.Contains(out, "survived") || cmd.ProcessState.ExitCode() != 0 {
		t.Errorf("nohup の下で、子が SIGHUP の無視を引き継いでいない: %q %v", out, cmd.ProcessState)
	}
	// SIGINT は受けて捨てる（子に渡さない）。
	if cmd, out := helper(t, "swallow"); !strings.Contains(out, "done") || cmd.ProcessState.ExitCode() != 0 {
		t.Errorf("SIGINT で子か run が止まった: %q %v", out, cmd.ProcessState)
	}
}
