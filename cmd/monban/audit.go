package main

// 最小のログ：リクエスト1回につき1行を、ファイルの末尾に足す。
// 形は1行1つの JSON。申告されたエージェント名は相手が決めるので、
// 改行などを混ぜて行を偽造されないよう、JSON の文字列として書く。
// ハッシュの連鎖は7段目で足す。それまでは、門番と同じ uid なら書き換えられる。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

type auditEntry struct {
	Time       string `json:"time"`
	ID         string `json:"id,omitempty"` // リクエストの ID。断ったときに相手に返すのはこれだけ
	UID        uint32 `json:"uid"`
	PID        int    `json:"pid"`
	Agent      string `json:"agent"`             // 申告されたもの。認証ではない
	Secret     string `json:"secret"`            // 名前だけ。値は書かない
	Command    string `json:"command,omitempty"` // 申告されたコマンドの絶対パス
	Argc       int    `json:"argc"`              // 引数の数。全文は書かない（別の秘密が入りうる）
	ArgsSHA256 string `json:"args_sha256,omitempty"`
	UserOwned  bool   `json:"user_owned,omitempty"` // ユーザーの持ち物の場所のコマンドを許した項目で通した
	Op         string `json:"op,omitempty"`         // approve・deny・show の接続（6段目）
	Target     string `json:"target,omitempty"`     // その接続が決めた要求のリクエストの ID。承認の番号は書かない
	Result     string `json:"result"`
}

type auditLog struct {
	mu sync.Mutex
	f  *os.File
}

func openAudit(path string) (*auditLog, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("ログのファイルは絶対パスにしてください: %s", path)
	}
	if _, err := trustedPath(filepath.Dir(path)); err != nil {
		return nil, fmt.Errorf("ログの置き場: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, fmt.Errorf("ログのファイル: %w", err)
	}
	st, err := f.Stat()
	if err == nil && (!st.Mode().IsRegular() || statUID(st) != uint32(os.Geteuid()) || st.Mode().Perm() != 0o600) {
		err = fmt.Errorf("ログのファイルが自分の持ち物の 0600 の普通のファイルではない: %s", path)
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	return &auditLog{f: f}, nil
}

// write は1行を足す。書けなければエラーを返す（呼び出し元は秘密を渡さない）。
func (a *auditLog) write(e auditEntry) error {
	e.Time = time.Now().Format(time.RFC3339Nano)
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	_, err = a.f.Write(append(b, '\n'))
	return err
}
