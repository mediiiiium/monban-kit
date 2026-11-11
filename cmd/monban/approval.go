package main

// 6段目：承認。
// 許可リストを通った要求は、すぐ渡さずに「待ち」にし、Pushover で知らせる。
// 人がターミナルで monban approve <番号> を打ったら、そこで初めて bws から取って渡す。
//
// 承認の番号は、リクエストの ID とは別のもの。番号は Pushover と門番の画面のログにだけ出し、run には返さない。
// 本番（_monban）では、ユーザーの uid からはそのどちらも読めない。ただし試すとき（自分の uid で動かす）は、
// 画面のログが読めるので、同じ uid のエージェントにも番号が見える（強くならない）。

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	approvalTimeout = 2 * time.Minute // 待ちの上限。過ぎたら断る
	maxPending      = 4               // 同時に待てる要求の数。超えたら断る（通知を溢れさせない）
	maxMisses       = 3               // 番号の外れがこの回数に達したら、待っている要求を全部断る
	codeDigits      = 8
)

// 待ちの結果。run への返事は、承認のほかはどれも同じ NG。
const (
	outApproved = "承認された"
	outDenied   = "拒否された"
	outTimeout  = "時間切れ"
	outGone     = "run が先に切れた"
	outMisses   = "番号の外れが続いたので、待ちを全部断った"
	outStopped  = "門番を止めた"
)

// pending は、承認を待っている要求1つ。
type pending struct {
	code    string
	id      string // リクエストの ID（run にも返している）
	agent   string
	secret  string
	command []string
	user    bool // userOwned の項目で通った
	pid     int
	since   time.Time
	done    chan string // 結果を1回だけ受け取る
}

// waitList は、待っている要求の並び。結果を決めるのは take で取り出した側だけ（二重に決めない）。
type waitList struct {
	mu     sync.Mutex
	items  []*pending
	misses int
}

var errTooMany = errors.New("待ちが多すぎる")

// add は新しい要求を待ちに入れる。番号は crypto/rand で作り、先頭のゼロを含む8桁の文字列にする。
func (w *waitList) add(p *pending) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.items) >= maxPending {
		return errTooMany
	}
	for {
		code, err := newCode()
		if err != nil {
			return err
		}
		if w.find(code) == nil {
			p.code = code
			break
		}
	}
	p.done = make(chan string, 1)
	w.items = append(w.items, p)
	return nil
}

// find は番号の一致する要求を探す（呼ぶ側が mu を持つ）。比べるのは長さの決まった文字列なので、時間の差で番号を探られにくい形にする。
func (w *waitList) find(code string) *pending {
	var hit *pending
	for _, p := range w.items {
		if subtle.ConstantTimeCompare([]byte(p.code), []byte(code)) == 1 {
			hit = p
		}
	}
	return hit
}

// remove は p を並びから外す。外せたら true（まだ誰も結果を決めていない）。
func (w *waitList) remove(p *pending) bool {
	for i, q := range w.items {
		if q == p {
			w.items = append(w.items[:i], w.items[i+1:]...)
			return true
		}
	}
	return false
}

// look は、番号の要求の写しを返す（承認の画面用）。外れなら数え、続けば全部断る。
func (w *waitList) look(code string) (pending, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	p := w.find(code)
	if p == nil {
		w.miss()
		return pending{}, false
	}
	w.misses = 0
	return *p, true
}

// decide は、番号の要求の結果を決める。外れなら数え、続けば全部断る。
func (w *waitList) decide(code, out string) (string, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	p := w.find(code)
	if p == nil {
		w.miss()
		return "", false
	}
	w.misses = 0
	w.remove(p)
	p.done <- out
	return p.id, true
}

// miss は外れを数える（呼ぶ側が mu を持つ）。maxMisses に達したら、待ちを全部断って数え直す。
// 正しい番号が使われたときも数え直す（人の打ち間違いが日をまたいで溜まらないように）。
// 当て続けるには新しい要求を出すしかなく、そのたびにスマホに知らせが行く。
func (w *waitList) miss() {
	w.misses++
	if w.misses < maxMisses {
		return
	}
	w.misses = 0
	for _, p := range w.items {
		p.done <- outMisses
	}
	w.items = nil
}

// wait は、結果が決まるか、時間切れか、run が切れるか、門番が止まるまで待つ。
func (w *waitList) wait(p *pending, timeout time.Duration, gone, stop <-chan struct{}) string {
	t := time.NewTimer(timeout)
	defer t.Stop()
	var out string
	select {
	case r := <-p.done:
		return r
	case <-t.C:
		out = outTimeout
	case <-gone:
		out = outGone
	case <-stop:
		out = outStopped
	}
	w.mu.Lock()
	ok := w.remove(p)
	w.mu.Unlock()
	if ok {
		return out
	}
	// 外す前に、ほかの誰かが結果を決めていた。
	return <-p.done
}

func newCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(100_000_000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%0*d", codeDigits, n.Int64()), nil
}

// validCode は、番号の形（8桁の数字）か。
func validCode(s string) bool {
	if len(s) != codeDigits {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// --- Pushover ---

// pushover は、承認待ちを知らせるだけ（承認か拒否かは返せない）。
// 送るのは番号・名札・秘密の名前・コマンドの絶対パス・引数の数。値と引数の全文は送らない。
// 名前とコマンドは、Pushover のサーバーとスマホに残る。
type pushover struct {
	token, user string
	endpoint    string
	client      *http.Client
}

// readPushover は、Pushover のアプリのトークンとユーザーキーのファイル（JSON、0600、0700 のディレクトリ）を読む。
func readPushover(path string) (*pushover, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("Pushover のファイルは絶対パスにしてください: %s", path)
	}
	if err := checkPrivate(filepath.Dir(path), 0o700, true); err != nil {
		return nil, fmt.Errorf("Pushover のファイルのディレクトリ: %w", err)
	}
	if err := checkPrivate(path, 0o600, false); err != nil {
		return nil, fmt.Errorf("Pushover のファイル: %w", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f struct {
		Token string `json:"token"`
		User  string `json:"user"`
	}
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil || f.Token == "" || f.User == "" {
		return nil, errors.New(`Pushover のファイルの中身が {"token": …, "user": …} ではない`)
	}
	return &pushover{token: f.Token, user: f.User, endpoint: "https://api.pushover.net/1/messages.json",
		client: &http.Client{Timeout: 10 * time.Second}}, nil
}

func pushoverMessage(p *pending) (title, body string) {
	title = "monban 承認待ち " + p.code
	var b strings.Builder
	fmt.Fprintf(&b, "番号 %s\n名札 %s\n秘密 %s\nコマンド %s（引数 %d）\n", p.code, clip(p.agent, 64), p.secret, p.command[0], len(p.command)-1)
	if p.user {
		b.WriteString("注意：ユーザーの持ち物の場所のコマンド\n")
	}
	fmt.Fprintf(&b, "%d分以内に monban approve %s", int(approvalTimeout/time.Minute), p.code)
	return title, b.String()
}

// send は知らせる。ttl を待ちの上限に揃え、スマホに残る番号の写しを早く消す。優先度は通常。
// エラーにはトークンを入れない（送る中身は本文で、URL には入れていない）。
func (po *pushover) send(ctx context.Context, p *pending) error {
	title, body := pushoverMessage(p)
	return po.post(ctx, title, body, approvalTimeout)
}

// note は、起動と停止の知らせ。ログの連鎖の写しを門番の外に残すので、ttl を付けない（消えないように）。
// 承認の番号は入らない。
func (po *pushover) note(ctx context.Context, title, body string) error {
	return po.post(ctx, title, body, 0)
}

func (po *pushover) post(ctx context.Context, title, body string, ttl time.Duration) error {
	form := url.Values{
		"token":   {po.token},
		"user":    {po.user},
		"title":   {title},
		"message": {body},
	}
	if ttl > 0 {
		form.Set("ttl", strconv.Itoa(int(ttl/time.Second)))
	}
	req, err := http.NewRequestWithContext(ctx, "POST", po.endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := po.client.Do(req)
	if err != nil {
		return errors.New("Pushover に繋げない")
	}
	defer resp.Body.Close()
	var r struct {
		Status int `json:"status"`
	}
	json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&r)
	if resp.StatusCode != http.StatusOK || r.Status != 1 {
		return fmt.Errorf("Pushover が受け取らなかった（HTTP %d）", resp.StatusCode)
	}
	return nil
}

// --- 承認の側の要求（approve / deny） ---

// shown は、承認の画面に出す、門番が作った事実。
type shown struct {
	ID      string   `json:"id"`
	Agent   string   `json:"agent"`
	Secret  string   `json:"secret"`
	Command []string `json:"command"`
	User    bool     `json:"userOwned"`
	PID     int      `json:"pid"`
	Waited  int      `json:"waited"` // 要求が来てからの秒
	Left    int      `json:"left"`   // 残りの秒
}

// handleDecision は、approve・deny・show の接続を扱う。外れたときの返事は理由を分けず「NG <この接続の ID>」。
func (s *server) handleDecision(c net.Conn, e auditEntry, p peer, req request, deny func(string)) {
	if !validCode(req.Code) {
		s.wait.mu.Lock()
		s.wait.miss()
		s.wait.mu.Unlock()
		deny("番号が外れた")
		return
	}
	switch req.Op {
	case "show":
		q, ok := s.wait.look(req.Code)
		if !ok {
			deny("番号が外れた")
			return
		}
		el := time.Since(q.since)
		b, _ := json.Marshal(shown{ID: q.id, Agent: q.agent, Secret: q.secret, Command: q.command, User: q.user, PID: q.pid,
			Waited: int(el / time.Second), Left: int((approvalTimeout - el) / time.Second)})
		e.Target, e.Result = q.id, "承認の画面に出した"
		s.audit.write(e)
		fmt.Fprintf(c, "SHOW %s\n", b)
	case "approve", "deny":
		out := outApproved
		if req.Op == "deny" {
			out = outDenied
		}
		id, ok := s.wait.decide(req.Code, out)
		if !ok {
			deny("番号が外れた")
			return
		}
		e.Target, e.Result = id, out
		if err := s.audit.write(e); err != nil {
			log.Printf("ログに書けない: %v", err)
		}
		log.Printf("%s id=%s（uid=%d pid=%d から）", out, id, p.uid, p.pid)
		fmt.Fprintf(c, "OK %s\n", id)
	default:
		deny("要求を読めない")
	}
}

// --- approve / deny のコマンド（人がターミナルで打つ側） ---

func runApprove(args []string) error {
	code, err := codeArg("approve", args)
	if err != nil {
		return err
	}
	line, err := decisionAsk(request{Op: "show", Code: code})
	if err != nil {
		return err
	}
	j, ok := strings.CutPrefix(line, "SHOW ")
	var sh shown
	if !ok || json.Unmarshal([]byte(j), &sh) != nil || len(sh.Command) == 0 {
		return errors.New("門番の返事を読めない")
	}
	printShown(os.Stdout, sh)
	fmt.Print("承認しますか [y/N]: ")
	ans, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	if strings.TrimSpace(ans) != "y" {
		fmt.Println("承認しなかった（待ちはそのまま。断るなら monban deny）")
		return nil
	}
	line, err = decisionAsk(request{Op: "approve", Code: code})
	if err != nil {
		return err
	}
	if id, ok := strings.CutPrefix(line, "OK "); ok {
		fmt.Printf("承認した（リクエストの ID %s）\n", id)
		return nil
	}
	return errors.New("門番の返事を読めない")
}

func runDeny(args []string) error {
	code, err := codeArg("deny", args)
	if err != nil {
		return err
	}
	line, err := decisionAsk(request{Op: "deny", Code: code})
	if err != nil {
		return err
	}
	if id, ok := strings.CutPrefix(line, "OK "); ok {
		fmt.Printf("拒否した（リクエストの ID %s）\n", id)
		return nil
	}
	return errors.New("門番の返事を読めない")
}

func codeArg(name string, args []string) (string, error) {
	if len(args) != 1 {
		return "", fmt.Errorf("使い方: monban %s <承認の番号（8桁）>", name)
	}
	if !validCode(args[0]) {
		return "", errors.New("承認の番号は8桁の数字です（先頭の 0 も打ってください）")
	}
	return args[0], nil
}

// decisionAsk は1行送って1行読む。NG なら理由を分けずに返す。
func decisionAsk(req request) (string, error) {
	c, err := net.DialTimeout("unix", socketPath(), 5*time.Second)
	if err != nil {
		return "", fmt.Errorf("門番に繋げない: %w", err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(30 * time.Second))
	b, _ := json.Marshal(req)
	if _, err := c.Write(append(b, '\n')); err != nil {
		return "", fmt.Errorf("門番に送れない: %w", err)
	}
	line, err := bufio.NewReader(io.LimitReader(c, maxRequest+maxValue)).ReadString('\n')
	if err != nil {
		return "", errors.New("門番の返事を読めない")
	}
	line = strings.TrimSuffix(line, "\n")
	if id, ok := strings.CutPrefix(line, "NG "); ok || line == "NG" {
		return "", fmt.Errorf("門番に断られた（番号が違うか、もう待っていない。ID %s）", id)
	}
	return line, nil
}

// printShown は承認の画面。名札と引数はエージェントが決めるので、端末の制御文字で画面を偽れないよう、
// どれも引用符つきのエスケープした形で出す。引数は全文を出す（ファイルのログには数とハッシュだけ）。
func printShown(w io.Writer, sh shown) {
	fmt.Fprintf(w, "リクエストの ID  %s\n", sh.ID)
	fmt.Fprintf(w, "名札（申告）     %s\n", strconv.Quote(sh.Agent))
	fmt.Fprintf(w, "秘密             %s\n", strconv.Quote(sh.Secret))
	fmt.Fprintf(w, "コマンド         %s\n", strconv.Quote(sh.Command[0]))
	if sh.User {
		fmt.Fprintln(w, "                 注意：ユーザーの持ち物の場所のコマンド（エージェントが中身を差し替えられる）")
	}
	for i, a := range sh.Command[1:] {
		fmt.Fprintf(w, "引数 %-2d          %s\n", i+1, strconv.Quote(a))
	}
	fmt.Fprintf(w, "繋いできた pid   %d\n", sh.PID)
	fmt.Fprintf(w, "経過 %d秒・残り %d秒\n", sh.Waited, max(sh.Left, 0))
}
