package main

// 許可リスト：どの秘密を、どのコマンドに、どの名札に渡してよいか（4段目）。
// 3段目の「名前 ID」の対応表を、この JSON のファイルにまとめた。
//
// 大事な前提：要求の中のコマンドと名札は、どちらも申告。
// 同じ uid のエージェントは、run を使わずにソケットへ直接書けば、許可されたコマンドを名乗れる。
// だから、ここでの照合は事故防止（うっかり別のコマンドや別のエージェントに渡すのを止める）と、
// ログと承認の材料で、悪意のあるエージェントへの守りではない。
//
// 形：
//
//	{
//	  "secrets": [
//	    {
//	      "name": "MONBAN_TEST",
//	      "id": "00000000-0000-0000-0000-000000000000",
//	      "allow": [
//	        {"command": "/usr/bin/printenv", "agents": ["*"]},
//	        {"command": "/usr/bin/curl", "agents": ["claude"], "hosts": ["api.example.com"]},
//	        {"command": "/opt/homebrew/bin/gh", "agents": ["human"], "userOwned": true}
//	      ]
//	    }
//	  ]
//	}
//
// - command：絶対パスの完全一致で照らす
// - agents：名札。"*" はだれでも（"*" は単独で書く）
// - hosts：書いたときだけ、https の URL の引数が1つ以上あり、そのホストが全部この中にあることを求める
// - userOwned：置き場が root か門番の持ち物でない（Homebrew など）ことを、本人が選んだ印。
//   印のない項目は、置き場が root か門番の持ち物でなければ起動しない

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

var (
	nameRe  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)
	uuidRe  = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	agentRe = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)
	hostRe  = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)*$`)
)

// 何でも実行できるコマンドは、許可リストに書けない（書かれていたら起動しない）。
// 許すと「どのコマンドに」という考え自体が崩れる。スクリプトは、そのファイルのパスを書く。
// この一覧は網羅ではない（例えば git や make も、設定しだいで何でも実行できる）。
var (
	deniedNames = map[string]bool{
		"sh": true, "bash": true, "zsh": true, "dash": true, "ksh": true, "csh": true, "tcsh": true, "fish": true,
		"env": true, "osascript": true, "swift": true, "expect": true, "irb": true, "jshell": true, "pwsh": true,
		"deno": true, "bun": true, "awk": true, "gawk": true, "nawk": true,
		"xargs": true, "find": true, "sudo": true, "su": true, "doas": true, "nohup": true, "nice": true,
		"time": true, "timeout": true, "script": true, "xcrun": true, "open": true, "arch": true, "launchctl": true,
		"busybox": true, "monban": true,
	}
	deniedPrefixRe = regexp.MustCompile(`^(python|pypy|perl|ruby|php|lua|tclsh|wish|node|nodejs)[0-9.]*$`)
)

func denied(path string) bool {
	b := filepath.Base(path)
	return deniedNames[b] || deniedPrefixRe.MatchString(b)
}

type allowRule struct {
	Command   string   `json:"command"`
	Agents    []string `json:"agents"`
	Hosts     []string `json:"hosts,omitempty"`
	UserOwned bool     `json:"userOwned,omitempty"`
}

type secretRule struct {
	Name  string      `json:"name"`
	ID    string      `json:"id"`
	Allow []allowRule `json:"allow"`
}

type allowlist struct {
	secrets map[string]*secretRule
}

// loadAllowlist は、許可リストを読み、各コマンドの置き場を確かめる。
// 本番では許可リストは root の持ち物の 0644（門番が自分で許可を広げられないように）。
// 試すときに自分の uid で動かす間は、root か自分の持ち物を許す。
func loadAllowlist(path string) (*allowlist, error) {
	real, err := trustedPath(path)
	if err != nil {
		return nil, fmt.Errorf("許可リスト: %w", err)
	}
	st, err := os.Lstat(real)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("許可リストが普通のファイルではない: %s", real)
	}
	b, err := os.ReadFile(real)
	if err != nil {
		return nil, err
	}
	a, err := parseAllowlist(b)
	if err != nil {
		return nil, err
	}
	for _, s := range a.secrets {
		for i, r := range s.Allow {
			if err := checkCommand(r.Command, r.UserOwned); err != nil {
				return nil, fmt.Errorf("許可リストの %s の %d 番目のコマンド: %w", s.Name, i+1, err)
			}
		}
	}
	return a, nil
}

// parseAllowlist は中身の形だけを確かめる（ファイルの置き場は見ない）。
func parseAllowlist(b []byte) (*allowlist, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields() // 書き間違えた欄を黙って無視しない
	var f struct {
		Secrets []*secretRule `json:"secrets"`
	}
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("許可リストを読めない: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("許可リストの後ろに余計なものがある")
	}
	if len(f.Secrets) == 0 {
		return nil, errors.New("許可リストが空")
	}
	a := &allowlist{secrets: map[string]*secretRule{}}
	ids := map[string]bool{}
	for n, s := range f.Secrets {
		where := fmt.Sprintf("許可リストの %d 番目の秘密", n+1)
		if s == nil {
			return nil, errors.New(where + "：空")
		}
		if !nameRe.MatchString(s.Name) {
			return nil, errors.New(where + "：名前に使えない文字がある")
		}
		where = "許可リストの " + s.Name
		s.ID = strings.ToLower(s.ID)
		if !uuidRe.MatchString(s.ID) {
			return nil, errors.New(where + "：ID が UUID の形ではない")
		}
		if a.secrets[s.Name] != nil {
			return nil, errors.New(where + "：名前が2回出てくる")
		}
		if ids[s.ID] {
			return nil, errors.New(where + "：ID がほかの秘密と同じ")
		}
		if len(s.Allow) == 0 {
			return nil, errors.New(where + "：allow が空")
		}
		cmds := map[string]bool{}
		for i := range s.Allow {
			r := &s.Allow[i]
			w := fmt.Sprintf("%s の %d 番目の allow", where, i+1)
			if !filepath.IsAbs(r.Command) || filepath.Clean(r.Command) != r.Command {
				return nil, fmt.Errorf("%s：command は整った絶対パスにしてください: %q", w, r.Command)
			}
			if denied(r.Command) {
				return nil, fmt.Errorf("%s：何でも実行できるコマンドは許可できない: %s", w, r.Command)
			}
			if cmds[r.Command] {
				return nil, fmt.Errorf("%s：同じコマンドが2回出てくる（名札は1つの項目にまとめてください）", w)
			}
			cmds[r.Command] = true
			if len(r.Agents) == 0 {
				return nil, fmt.Errorf("%s：agents が空（だれでもなら [\"*\"]）", w)
			}
			for _, ag := range r.Agents {
				if ag == "*" {
					if len(r.Agents) != 1 {
						return nil, fmt.Errorf("%s：\"*\" はほかの名札と並べない", w)
					}
					continue
				}
				if !agentRe.MatchString(ag) {
					return nil, fmt.Errorf("%s：名札に使えない文字がある: %q", w, ag)
				}
			}
			for _, h := range r.Hosts {
				if len(h) > 253 || !hostRe.MatchString(h) {
					return nil, fmt.Errorf("%s：hosts は小文字のホスト名にしてください: %q", w, h)
				}
			}
			if r.Hosts != nil && len(r.Hosts) == 0 {
				return nil, fmt.Errorf("%s：hosts が空（付けないなら欄ごと消してください）", w)
			}
		}
		ids[s.ID] = true
		a.secrets[s.Name] = s
	}
	return a, nil
}

// checkCommand は、許可するコマンドの置き場を確かめる。
// 印（userOwned）のない項目は、bws と同じく、本当の場所と上のディレクトリが root か自分の持ち物で、
// グループとほかの人から書けないこと。書かれたパスのディレクトリも見る（シンボリックリンクの差し替えを防ぐ）。
// 印のある項目は、そうでないことを確かめる（要らない印を残さない）。
func checkCommand(path string, userOwned bool) error {
	_, dirErr := trustedPath(filepath.Dir(path))
	real, err := trustedPath(path)
	strictOK := dirErr == nil && err == nil
	if userOwned {
		if strictOK {
			return errors.New("置き場は root か門番の持ち物なので、userOwned の印は要らない")
		}
		if real, err = filepath.EvalSymlinks(path); err != nil {
			return err
		}
	} else if !strictOK {
		if dirErr != nil {
			err = dirErr
		}
		return fmt.Errorf("置き場: %w（Homebrew などユーザーの持ち物の場所なら、\"userOwned\": true を付ける）", err)
	}
	if denied(real) {
		return fmt.Errorf("本当の場所が、何でも実行できるコマンド: %s", real)
	}
	st, err := os.Stat(real)
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() || st.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("実行できる普通のファイルではない: %s", real)
	}
	return nil
}

// weakRules は、印（userOwned）の付いた項目を返す。起動時の画面とログに出す。
func (a *allowlist) weakRules() [][2]string {
	var out [][2]string
	for _, s := range a.secrets {
		for _, r := range s.Allow {
			if r.UserOwned {
				out = append(out, [2]string{s.Name, r.Command})
			}
		}
	}
	slices.SortFunc(out, func(x, y [2]string) int { return strings.Compare(x[0]+"\x00"+x[1], y[0]+"\x00"+y[1]) })
	return out
}

const maxArgs = 64

// check は要求を照らし、通すなら秘密の ID を返す。
// 断る理由（reason）は門番のログにだけ書く。相手には返さない（許可リストの中身を探られないように）。
func (a *allowlist) check(agent, secret string, command []string) (id string, rule *allowRule, reason string) {
	if len(command) == 0 || len(command) > maxArgs+1 {
		return "", nil, "コマンドの形が違う"
	}
	for _, s := range command {
		if strings.IndexByte(s, 0) >= 0 {
			return "", nil, "コマンドの形が違う"
		}
	}
	if !filepath.IsAbs(command[0]) || filepath.Clean(command[0]) != command[0] {
		return "", nil, "コマンドが整った絶対パスではない"
	}
	s := a.secrets[secret]
	if s == nil {
		return "", nil, "許可リストにない秘密"
	}
	for i := range s.Allow {
		r := &s.Allow[i]
		if r.Command != command[0] {
			continue
		}
		if r.Agents[0] != "*" && !slices.Contains(r.Agents, agent) {
			return "", nil, "この名札には許していない"
		}
		if r.Hosts != nil {
			if why := checkHosts(command[1:], r.Hosts); why != "" {
				return "", nil, why
			}
		}
		return s.ID, r, ""
	}
	return "", nil, "このコマンドには許していない"
}

// checkHosts は、送り先の URL のホストが許可したものだけかを確かめる。
// 「://」を含む引数を URL とみなす（--url=… の形なら = の後ろ）。https だけを許す。
// これで防げないこと：curl は「://」のないホスト名も URL として読む。プロキシや設定ファイルの指定もある。
// curl と Go で URL の読み方が違うこともある。だからこれも事故防止の範囲。
func checkHosts(args, hosts []string) string {
	found := false
	for _, a := range args {
		if !strings.Contains(a, "://") {
			continue
		}
		v := a
		if strings.HasPrefix(v, "-") {
			_, after, ok := strings.Cut(v, "=")
			if !ok {
				return "送り先の URL を読めない"
			}
			v = after
		}
		u, err := url.Parse(v)
		if err != nil || u.Scheme != "https" || u.Opaque != "" || u.User != nil || u.Hostname() == "" {
			return "送り先の URL を読めないか、https ではない"
		}
		if !slices.Contains(hosts, strings.ToLower(u.Hostname())) {
			return "送り先のホストを許していない"
		}
		found = true
	}
	if !found {
		return "送り先の URL がない"
	}
	return ""
}
