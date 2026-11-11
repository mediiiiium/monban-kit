package main

// ログ：1行1つの JSON を、ファイルの末尾に足す。申告されたエージェント名は相手が決めるので、
// 改行などを混ぜて行を偽造されないよう、JSON の文字列として書く。
//
// 7段目：ハッシュの連鎖。各行に通し番号（seq）と、1つ前の行のバイト列の SHA-256（prev）を入れる。
// ハッシュは、ファイルに書いた1行のバイト列そのもの（改行は除く）から取る。JSON を整え直さない。
// 鍵はない。書き換えられる人は、書き換えた後に連鎖を全部計算し直せる。
// わかるのは、壊れと、計算し直さない書き換え（途中の行を直した・消した・入れた）まで。
// 末尾の切り捨ては、起動と停止の行と、Pushover に送った起動と停止の知らせとの照合で、気づける場合がある。

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

type auditEntry struct {
	Seq        uint64 `json:"seq"`  // 1から始まる通し番号
	Prev       string `json:"prev"` // 1つ前の行のバイト列の SHA-256（最初の行はゼロ）
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
	Op         string `json:"op,omitempty"`         // approve・deny・show の接続（6段目）、門番の start・stop（7段目）、monban stop の接続 kill（8段目）
	Target     string `json:"target,omitempty"`     // その接続が決めた要求のリクエストの ID。承認の番号は書かない
	Result     string `json:"result"`
}

const (
	opStart = "start"
	opStop  = "stop"
)

var zeroHash = strings.Repeat("0", 64)

// maxLogLine は、確かめるときに読む1行の上限。門番が書く行はこれよりずっと短い。
const maxLogLine = 1 << 20

// chainState は、確かめ終わったところ。
type chainState struct {
	Lines  uint64 // 完全な行の数（＝最後の seq）
	Last   string // 最後の行のハッシュ（行がなければゼロ）
	LastOp string // 最後の行の op
	End    int64  // 最後の完全な行の改行までのバイト数
}

// chainError は、連鎖が合わないところ。Partial は「改行で終わっていない（書きかけ）」。
type chainError struct {
	Line    uint64 // 何行目か（1から）
	Partial bool
	State   chainState // そこまでで確かめられたところ
	Why     string
}

func (e *chainError) Error() string {
	if e.Partial {
		return fmt.Sprintf("最後の行が書きかけ（%d バイト目より後ろ）。完全な記録は %d 行で、失われていない。"+
			"最後の改行の後ろの断片だけを取り除けば起動できる（門番は自分では取り除かない）", e.State.End, e.State.Lines)
	}
	return fmt.Sprintf("%d 行目で連鎖が合わない：%s（その前の %d 行までは合っている）", e.Line, e.Why, e.State.Lines)
}

// lineHash は、1行（改行を除く）のハッシュ。
func lineHash(line []byte) string {
	h := sha256.Sum256(line)
	return hex.EncodeToString(h[:])
}

// verifyChain は、ログを頭から確かめる。at が 0 でなければ、その seq の行のハッシュも返す。
func verifyChain(r io.Reader, at uint64) (chainState, string, error) {
	st := chainState{Last: zeroHash}
	var atHash string
	br := bufio.NewReaderSize(r, 64*1024)
	for {
		line, err := readLogLine(br)
		if err == io.EOF {
			if at != 0 && atHash == "" {
				return st, "", fmt.Errorf("seq %d の行はない（最後は %d）", at, st.Lines)
			}
			return st, atHash, nil
		}
		n := st.Lines + 1
		if err == io.ErrUnexpectedEOF {
			return st, atHash, &chainError{Line: n, Partial: true, State: st}
		}
		if err != nil {
			return st, atHash, &chainError{Line: n, State: st, Why: err.Error()}
		}
		var e struct {
			Seq  *uint64 `json:"seq"`
			Prev *string `json:"prev"`
			Op   string  `json:"op"`
		}
		if err := json.Unmarshal(line, &e); err != nil {
			return st, atHash, &chainError{Line: n, State: st, Why: "JSON として読めない"}
		}
		if e.Seq == nil || e.Prev == nil {
			return st, atHash, &chainError{Line: n, State: st, Why: "seq か prev の欄がない（連鎖より前の形のログ）"}
		}
		if *e.Seq != n {
			return st, atHash, &chainError{Line: n, State: st, Why: fmt.Sprintf("seq が %d（%d のはず）", *e.Seq, n)}
		}
		if *e.Prev != st.Last {
			return st, atHash, &chainError{Line: n, State: st, Why: "prev が1つ前の行のハッシュと違う"}
		}
		st.Lines = n
		st.Last = lineHash(line)
		st.LastOp = e.Op
		st.End += int64(len(line)) + 1
		if n == at {
			atHash = st.Last
		}
	}
}

// readLogLine は、改行までの1行を返す（改行は除く）。
// 改行の前でファイルが終わったら io.ErrUnexpectedEOF。
func readLogLine(br *bufio.Reader) ([]byte, error) {
	var buf []byte
	for {
		part, err := br.ReadSlice('\n')
		buf = append(buf, part...)
		if len(buf) > maxLogLine {
			return nil, errors.New("1行が長すぎる")
		}
		switch err {
		case nil:
			return buf[:len(buf)-1], nil
		case bufio.ErrBufferFull:
			continue
		case io.EOF:
			if len(buf) == 0 {
				return nil, io.EOF
			}
			return nil, io.ErrUnexpectedEOF
		default:
			return nil, err
		}
	}
}

type auditLog struct {
	mu   sync.Mutex
	f    *os.File
	seq  uint64
	last string
	err  error // 一度書き損じたか、止めた後。それからは何も書かない（呼び出し元は秘密を渡さない）
}

// openAudit は、ログを開き、今ある連鎖を確かめる。合わなければ開かない（人に判断を任せる）。
// モードは 0600 か、0640 でグループが group（ソケットと同じ専用グループ）のとき。
// 返す chainState は、開いた時点の最後。
func openAudit(path string, group uint32) (*auditLog, chainState, error) {
	if !filepath.IsAbs(path) {
		return nil, chainState{}, fmt.Errorf("ログのファイルは絶対パスにしてください: %s", path)
	}
	if _, err := trustedPath(filepath.Dir(path)); err != nil {
		return nil, chainState{}, fmt.Errorf("ログの置き場: %w", err)
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_APPEND|os.O_CREATE|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, chainState{}, fmt.Errorf("ログのファイル: %w", err)
	}
	fail := func(err error) (*auditLog, chainState, error) {
		f.Close()
		return nil, chainState{}, err
	}
	fi, err := f.Stat()
	if err != nil {
		return fail(err)
	}
	perm := fi.Mode().Perm()
	gid := fi.Sys().(*syscall.Stat_t).Gid
	if !fi.Mode().IsRegular() || statUID(fi) != uint32(os.Geteuid()) ||
		!(perm == 0o600 || (perm == 0o640 && gid == group)) {
		return fail(fmt.Errorf("ログのファイルが、自分の持ち物の普通のファイルで 0600（か、ソケットと同じグループの 0640）ではない: %s", path))
	}
	st, _, err := verifyChain(f, 0)
	if err != nil {
		return fail(fmt.Errorf("ログ %s: %w", path, err))
	}
	return &auditLog{f: f, seq: st.Lines, last: st.Last}, st, nil
}

// write は1行を足す。書けなければエラーを返す（呼び出し元は秘密を渡さない）。
// 一度書き損じたら、それからは書かない（書きかけの断片の後ろに足すと、連鎖が読めなくなる）。
func (a *auditLog) write(e auditEntry) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.writeLocked(e)
}

func (a *auditLog) writeLocked(e auditEntry) error {
	if a.err != nil {
		return a.err
	}
	e.Seq = a.seq + 1
	e.Prev = a.last
	e.Time = time.Now().Format(time.RFC3339Nano)
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if bytes.IndexByte(b, '\n') >= 0 { // json.Marshal は改行をエスケープするので、ここには来ない
		return errors.New("行に改行がある")
	}
	if _, err := a.f.Write(append(b, '\n')); err != nil {
		a.err = fmt.Errorf("前に書き損じた: %w", err)
		return err
	}
	a.seq = e.Seq
	a.last = lineHash(b)
	return nil
}

// head は、最後の行の seq とハッシュ。
func (a *auditLog) head() (uint64, string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.seq, a.last
}

// close は、停止の行を書いて閉じる。それからは書かない。
func (a *auditLog) close(e auditEntry) (uint64, string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	e.Op = opStop
	err := a.writeLocked(e)
	if a.err == nil {
		a.err = errors.New("ログはもう閉じた")
	}
	a.f.Close()
	return a.seq, a.last, err
}

// --- monban verify-log ---

func runVerifyLog(args []string) error {
	var at uint64
	var path string
	switch {
	case len(args) == 1:
		path = args[0]
	case len(args) == 3 && args[0] == "-at":
		if _, err := fmt.Sscan(args[1], &at); err != nil || at == 0 {
			return errors.New("-at には 1 以上の seq を書いてください")
		}
		path = args[2]
	default:
		return errors.New("使い方: monban verify-log [-at <seq>] <ログのファイル>")
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	st, h, err := verifyChain(f, at)
	if err != nil {
		return err
	}
	fmt.Printf("連鎖は合っている：%d 行、%d バイト\n", st.Lines, st.End)
	if st.Lines > 0 {
		fmt.Printf("最後の行：seq %d ハッシュ %s（op %q）\n", st.Lines, st.Last, st.LastOp)
		if st.LastOp != opStop {
			fmt.Println("注意：最後の行が停止の行ではない（門番が動いているか、落ちたか、末尾が消された）")
		}
	}
	if at != 0 {
		fmt.Printf("seq %d の行のハッシュ：%s\n", at, h)
	}
	fmt.Println("連鎖が合っていても、書き換えた後に全部計算し直されていればわからない。Pushover の起動と停止の知らせの seq とハッシュと、ときどき照らしてください")
	return nil
}
