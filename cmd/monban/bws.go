package main

// bws を子プロセスで呼ぶ。
// - bws は絶対パスで呼び、起動時に署名（Bitwarden の TeamIdentifier）と置き場の持ち主を確かめる
// - 合言葉は環境変数で渡す。ほかの環境変数は渡さない
// - 値は呼び出し元に返すだけで、ログにもエラーにも出さない

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// bwsRequirement は、Bitwarden Inc の Developer ID で署名されていること。
const bwsRequirement = `anchor apple generic and identifier "bws" and certificate leaf[subject.OU] = "LTZ2PFU5D6"`

type bwsClient struct {
	path  string // 確かめた本当の場所
	token string
	home  string // bws の状態ファイルの置き場（HOME として渡す）
}

// checkBws は、bws の置き場をたどり、署名を確かめて、本当の場所を返す。
// 置き場を root か自分しか書き換えられないことは trustedPath で確かめる。
// 確かめた後に差し替えられないための条件なので、署名だけでは足りない。
func checkBws(path string) (string, error) {
	real, err := trustedPath(path)
	if err != nil {
		return "", fmt.Errorf("bws の置き場: %w", err)
	}
	cmd := exec.Command("/usr/bin/codesign", "--verify", "--strict", "-R="+bwsRequirement, real)
	cmd.Env = []string{}
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("bws の署名が Bitwarden のものではない: %s", strings.TrimSpace(string(out)))
	}
	return real, nil
}

// readToken は、0700 のディレクトリの中の 0600 のファイルから合言葉を1回だけ読む。
// どちらも自分の持ち物でなければ読まない。
func readToken(path string) (token, dir string, err error) {
	if !filepath.IsAbs(path) {
		return "", "", fmt.Errorf("合言葉のファイルは絶対パスにしてください: %s", path)
	}
	dir = filepath.Dir(path)
	if err := checkPrivate(dir, 0o700, true); err != nil {
		return "", "", fmt.Errorf("合言葉のディレクトリ: %w", err)
	}
	if err := checkPrivate(path, 0o600, false); err != nil {
		return "", "", fmt.Errorf("合言葉のファイル: %w", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", "", err
	}
	token = strings.TrimSpace(string(b))
	if token == "" || strings.ContainsAny(token, " \t\r\n") {
		return "", "", errors.New("合言葉のファイルの中身が1行の合言葉ではない")
	}
	return token, dir, nil
}

var errBws = errors.New("bws が失敗した")

// get は、ID の秘密を取り出し、名前が合っているかも確かめて、値を返す。
// エラーには値も合言葉も入れない。
func (b *bwsClient) get(ctx context.Context, name, id string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, b.path, "secret", "get", id, "--output", "json", "--color", "no")
	cmd.Env = []string{"BWS_ACCESS_TOKEN=" + b.token, "HOME=" + b.home}
	cmd.Dir = b.home
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		// stderr はエラーの説明だけのはずだが、念のため最初の段落（「Error:」と理由）・200 バイトまでにする。
		para, _, _ := strings.Cut(strings.TrimSpace(stderr.String()), "\n\n")
		msg := strings.Join(strings.Fields(para), " ")
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return "", fmt.Errorf("%w（%v）: %s", errBws, err, msg)
	}
	var s struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &s); err != nil {
		return "", fmt.Errorf("%w：出力を読めない", errBws)
	}
	if s.Key != name {
		return "", fmt.Errorf("%w：取れた秘密の名前が対応表と違う", errBws)
	}
	return s.Value, nil
}
