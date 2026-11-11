package main

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestReadReply(t *testing.T) {
	for _, v := range []string{"dummy", "", "改行\nと 空白\tを含む", strings.Repeat("x", maxValue)} {
		got, err := readReply(strings.NewReader(okReply("abc123", v)))
		if err != nil || got != v {
			t.Errorf("往復しない: err=%v 長さ %d→%d", err, len(v), len(got))
		}
	}
	for _, bad := range []string{
		"",
		"NG abc123\n",
		"NG\n",
		"OK abc123\n",     // 値がない
		"OK abc123 !!!\n", // base64 ではない
		"OK abc123 " + base64.StdEncoding.EncodeToString([]byte("a\x00b")) + "\n",                        // NUL
		"OK abc123 " + base64.StdEncoding.EncodeToString([]byte(strings.Repeat("x", maxValue+1))) + "\n", // 大きすぎる
		"HELLO\n",
	} {
		_, err := readReply(strings.NewReader(bad))
		if err == nil {
			t.Errorf("通ってしまった: %.40q", bad)
			continue
		}
		if strings.Contains(err.Error(), "a\x00b") {
			t.Errorf("エラーに値が出た")
		}
	}
	if _, err := readReply(strings.NewReader("NG abc123\n")); err == nil || !strings.Contains(err.Error(), "abc123") {
		t.Errorf("断られたときに ID を出していない: %v", err)
	}
}

func TestInjectEnv(t *testing.T) {
	got := injectEnv([]string{"PATH=/usr/bin", "MONBAN_TEST=old", "MONBAN_TEST_X=keep", "MONBAN_TEST=old2"}, "MONBAN_TEST", "new")
	want := []string{"PATH=/usr/bin", "MONBAN_TEST_X=keep", "MONBAN_TEST=new"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %q", got)
	}
}

func TestReadReplyWait(t *testing.T) {
	v, err := readReply(strings.NewReader("WAIT abc\nOK abc " + base64.StdEncoding.EncodeToString([]byte("x")) + "\n"))
	if err != nil || v != "x" {
		t.Fatalf("WAIT の後の値を読めない: %q %v", v, err)
	}
	for _, bad := range []string{"WAIT abc\n", "WAIT abc\nNG abc\n", "WAIT a\nWAIT b\nOK b eA==\n"} {
		if _, err := readReply(strings.NewReader(bad)); err == nil {
			t.Errorf("通ってしまった: %q", bad)
		}
	}
}
