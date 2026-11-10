package main

import (
	"strings"
	"testing"
)

func TestParseSecrets(t *testing.T) {
	m, err := parseSecrets(strings.NewReader("# コメント\n\nMONBAN_TEST 0A1B2C3D-0000-0000-0000-000000000000  # 後ろのコメント\n"))
	if err != nil {
		t.Fatal(err)
	}
	if m["MONBAN_TEST"] != "0a1b2c3d-0000-0000-0000-000000000000" {
		t.Fatalf("got %v", m)
	}
	for _, bad := range []string{
		"",                         // 空
		"MONBAN_TEST\n",            // ID がない
		"MONBAN_TEST not-a-uuid\n", // ID の形
		"MON-BAN 00000000-0000-0000-0000-000000000000\n",                                   // 名前の文字
		"A 00000000-0000-0000-0000-000000000000\nA 00000000-0000-0000-0000-000000000001\n", // 2回
	} {
		if _, err := parseSecrets(strings.NewReader(bad)); err == nil {
			t.Errorf("通ってしまった: %q", bad)
		}
	}
}

func TestClip(t *testing.T) {
	if got := clip("あいうえお", 3); got != "あいう…" {
		t.Fatalf("got %q", got)
	}
	if got := clip("abc", 3); got != "abc" {
		t.Fatalf("got %q", got)
	}
}
