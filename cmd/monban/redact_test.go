package main

import (
	"bytes"
	"math/rand"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
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

const testValue = "dummy-value-for-test"

func childOf(t *testing.T, script string) (childResult, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	env := injectEnv(os.Environ(), "MONBAN_TEST", testValue)
	res, err := runChild("/bin/sh", []string{"/bin/sh", "-c", script}, env, testValue, nil, &stdout, &stderr, nil)
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
	if res.code != 3 || res.signal != 0 || res.redacted != 2 {
		t.Fatalf("終わり方: %+v", res)
	}
}

func TestRunChildSignal(t *testing.T) {
	res, _, _ := childOf(t, `kill -TERM $$`)
	if res.signal != syscall.SIGTERM || res.code != -1 {
		t.Fatalf("終わり方: %+v", res)
	}
}

// 子が孫を残して出力を握っていても、子が終わってから2秒ほどで戻る。
func TestRunChildGrandchildHoldsPipe(t *testing.T) {
	res, out, _ := childOf(t, `(sleep 10; echo late) & echo now`)
	if res.code != 0 || out != "now\n" {
		t.Fatalf("終わり方: %+v 出力 %q", res, out)
	}
}

// 子に渡す合図：run が受けた合図は子に届く。
func TestRunChildForwardsSignal(t *testing.T) {
	sigs := make(chan os.Signal, 1)
	sigs <- syscall.SIGHUP
	var stdout bytes.Buffer
	res, err := runChild("/bin/sh", []string{"/bin/sh", "-c", "sleep 5"}, os.Environ(), testValue, nil, &stdout, &stdout, sigs)
	if err != nil {
		t.Fatal(err)
	}
	if res.signal != syscall.SIGHUP {
		t.Fatalf("終わり方: %+v", res)
	}
}

// run 自身の終わり方：子が SIGTERM で死んだら run も SIGTERM で死に、それ以外の合図は 128＋番号。
// exitLike はプロセスを終えるので、このテストのバイナリを子として動かして確かめる。
func TestExitLike(t *testing.T) {
	if s := os.Getenv("MONBAN_EXITLIKE"); s != "" {
		switch s {
		case "term":
			exitLike(childResult{code: -1, signal: syscall.SIGTERM})
		case "usr1":
			exitLike(childResult{code: -1, signal: syscall.SIGUSR1})
		case "code":
			exitLike(childResult{code: 7})
		}
		return
	}
	for _, c := range []struct {
		mode string
		sig  syscall.Signal
		code int
	}{{"term", syscall.SIGTERM, -1}, {"usr1", 0, 128 + int(syscall.SIGUSR1)}, {"code", 0, 7}} {
		cmd := exec.Command(os.Args[0], "-test.run=^TestExitLike$")
		cmd.Env = append(os.Environ(), "MONBAN_EXITLIKE="+c.mode)
		cmd.Run()
		ws := cmd.ProcessState.Sys().(syscall.WaitStatus)
		if c.sig != 0 {
			if !ws.Signaled() || ws.Signal() != c.sig {
				t.Errorf("%s: 合図 %v で死んでいない（%v）", c.mode, c.sig, cmd.ProcessState)
			}
		} else if ws.Signaled() || ws.ExitStatus() != c.code {
			t.Errorf("%s: 終了コード %d ではない（%v）", c.mode, c.code, cmd.ProcessState)
		}
	}
}
