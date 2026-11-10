package main

import (
	"strings"
	"testing"
)

const goodList = `{
  "secrets": [
    {
      "name": "MONBAN_TEST",
      "id": "0A1B2C3D-0000-0000-0000-000000000000",
      "allow": [
        {"command": "/usr/bin/printenv", "agents": ["*"]},
        {"command": "/usr/bin/curl", "agents": ["claude", "human"], "hosts": ["api.example.com"]}
      ]
    }
  ]
}`

func TestParseAllowlist(t *testing.T) {
	a, err := parseAllowlist([]byte(goodList))
	if err != nil {
		t.Fatal(err)
	}
	if a.secrets["MONBAN_TEST"].ID != "0a1b2c3d-0000-0000-0000-000000000000" {
		t.Fatalf("ID を小文字にしていない: %v", a.secrets["MONBAN_TEST"].ID)
	}
	rule := func(r string) string {
		return `{"secrets":[{"name":"A","id":"00000000-0000-0000-0000-000000000000","allow":[` + r + `]}]}`
	}
	for _, bad := range []string{
		``,
		`{}`,
		`{"secrets":[]}`,
		`{"secrets":[null]}`,
		goodList + `{}`, // 後ろに余計なもの
		`{"secrets":[{"name":"A","id":"00000000-0000-0000-0000-000000000000","allow":[{"command":"/usr/bin/printenv","agents":["*"]}],"extra":1}]}`, // 知らない欄
		`{"secrets":[{"name":"MON-BAN","id":"00000000-0000-0000-0000-000000000000","allow":[{"command":"/usr/bin/printenv","agents":["*"]}]}]}`,
		`{"secrets":[{"name":"A","id":"not-a-uuid","allow":[{"command":"/usr/bin/printenv","agents":["*"]}]}]}`,
		`{"secrets":[{"name":"A","id":"00000000-0000-0000-0000-000000000000","allow":[]}]}`,
		`{"secrets":[{"name":"A","id":"00000000-0000-0000-0000-000000000000","allow":[{"command":"/usr/bin/printenv","agents":["*"]}]},` +
			`{"name":"A","id":"00000000-0000-0000-0000-000000000001","allow":[{"command":"/usr/bin/printenv","agents":["*"]}]}]}`, // 名前が2回
		`{"secrets":[{"name":"A","id":"00000000-0000-0000-0000-000000000000","allow":[{"command":"/usr/bin/printenv","agents":["*"]}]},` +
			`{"name":"B","id":"00000000-0000-0000-0000-000000000000","allow":[{"command":"/usr/bin/printenv","agents":["*"]}]}]}`, // ID が2回
		rule(`{"command":"printenv","agents":["*"]}`),                                                         // 絶対パスではない
		rule(`{"command":"/usr/bin/../bin/printenv","agents":["*"]}`),                                         // 整っていない
		rule(`{"command":"/bin/sh","agents":["*"]}`),                                                          // 何でも実行できる
		rule(`{"command":"/usr/bin/env","agents":["*"]}`),                                                     // 何でも実行できる
		rule(`{"command":"/usr/bin/python3","agents":["*"]}`),                                                 // 何でも実行できる
		rule(`{"command":"/opt/homebrew/bin/node","agents":["*"]}`),                                           // 何でも実行できる
		rule(`{"command":"/usr/bin/printenv","agents":[]}`),                                                   // 名札がない
		rule(`{"command":"/usr/bin/printenv","agents":["*","claude"]}`),                                       // * と並べる
		rule(`{"command":"/usr/bin/printenv","agents":["a b"]}`),                                              // 名札の文字
		rule(`{"command":"/usr/bin/curl","agents":["*"],"hosts":[]}`),                                         // hosts が空
		rule(`{"command":"/usr/bin/curl","agents":["*"],"hosts":["API.example.com"]}`),                        // 大文字
		rule(`{"command":"/usr/bin/curl","agents":["*"],"hosts":["a.com/x"]}`),                                // ホスト名ではない
		rule(`{"command":"/usr/bin/printenv","agents":["*"]},{"command":"/usr/bin/printenv","agents":["x"]}`), // 同じコマンドが2回
	} {
		if _, err := parseAllowlist([]byte(bad)); err == nil {
			t.Errorf("通ってしまった: %s", bad)
		}
	}
}

func TestCheck(t *testing.T) {
	a, err := parseAllowlist([]byte(goodList))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		agent, secret string
		command       []string
		want          string // 空なら通る
	}{
		{"claude", "MONBAN_TEST", []string{"/usr/bin/printenv", "MONBAN_TEST"}, ""},
		{"だれでも", "MONBAN_TEST", []string{"/usr/bin/printenv"}, ""},
		{"claude", "NOPE", []string{"/usr/bin/printenv"}, "許可リストにない秘密"},
		{"claude", "MONBAN_TEST", []string{"/bin/cat"}, "このコマンドには許していない"},
		{"claude", "MONBAN_TEST", []string{"/usr/bin/../bin/printenv"}, "コマンドが整った絶対パスではない"},
		{"claude", "MONBAN_TEST", []string{"printenv"}, "コマンドが整った絶対パスではない"},
		{"claude", "MONBAN_TEST", nil, "コマンドの形が違う"},
		{"claude", "MONBAN_TEST", []string{"/usr/bin/printenv", "a\x00b"}, "コマンドの形が違う"},
		{"claude", "MONBAN_TEST", []string{"/usr/bin/curl", "https://api.example.com/v1"}, ""},
		{"human", "MONBAN_TEST", []string{"/usr/bin/curl", "-s", "--url=https://API.example.com:443/v1"}, ""},
		{"codex", "MONBAN_TEST", []string{"/usr/bin/curl", "https://api.example.com/v1"}, "この名札には許していない"},
		{"claude", "MONBAN_TEST", []string{"/usr/bin/curl", "https://evil.example.net/"}, "送り先のホストを許していない"},
		{"claude", "MONBAN_TEST", []string{"/usr/bin/curl", "https://api.example.com/", "https://evil.example.net/"}, "送り先のホストを許していない"},
		{"claude", "MONBAN_TEST", []string{"/usr/bin/curl", "https://api.example.com@evil.example.net/"}, "送り先の URL を読めないか、https ではない"},
		{"claude", "MONBAN_TEST", []string{"/usr/bin/curl", "http://api.example.com/"}, "送り先の URL を読めないか、https ではない"},
		{"claude", "MONBAN_TEST", []string{"/usr/bin/curl", "-s"}, "送り先の URL がない"},
		{"claude", "MONBAN_TEST", []string{"/usr/bin/curl", "--url", "https://api.example.com/"}, ""},
	}
	for _, c := range cases {
		id, _, why := a.check(c.agent, c.secret, c.command)
		if why != c.want {
			t.Errorf("%v %q %q: 理由 %q、期待 %q", c.command, c.agent, c.secret, why, c.want)
		}
		if why == "" && id != "0a1b2c3d-0000-0000-0000-000000000000" {
			t.Errorf("%v: ID %q", c.command, id)
		}
	}
	long := make([]string, maxArgs+2)
	long[0] = "/usr/bin/printenv"
	if _, _, why := a.check("x", "MONBAN_TEST", long); why != "コマンドの形が違う" {
		t.Errorf("引数が多すぎても通った: %q", why)
	}
}

func TestCheckCommand(t *testing.T) {
	// /usr/bin/printenv は root の持ち物の場所にある。
	if err := checkCommand("/usr/bin/printenv", false); err != nil {
		t.Fatal(err)
	}
	if err := checkCommand("/usr/bin/printenv", true); err == nil || !strings.Contains(err.Error(), "要らない") {
		t.Fatalf("要らない印を通した: %v", err)
	}
	if err := checkCommand("/usr/bin/no-such-command", false); err == nil {
		t.Fatal("ないコマンドを通した")
	}
	// /etc/hosts は実行できない。
	if err := checkCommand("/etc/hosts", false); err == nil {
		t.Fatal("実行できないファイルを通した")
	}
}

func TestRedact(t *testing.T) {
	if got := redact("Error: token abc123 is bad abc123", "abc123", ""); got != "Error: token [伏せた] is bad [伏せた]" {
		t.Fatalf("got %q", got)
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
