package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newPending(id string) *pending {
	return &pending{id: id, agent: "claude", secret: "MONBAN_TEST", command: []string{"/usr/bin/curl", "-H", "Authorization: x", "https://api.example.com/"}, since: time.Now()}
}

func TestCode(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 2000; i++ {
		c, err := newCode()
		if err != nil || !validCode(c) {
			t.Fatalf("番号の形が違う: %q %v", c, err)
		}
		seen[c] = true
	}
	if len(seen) < 1990 {
		t.Fatalf("番号が重なりすぎる: %d", len(seen))
	}
	for _, bad := range []string{"", "1234567", "123456789", "1234567a", "１２３４５６７８", " 1234567"} {
		if validCode(bad) {
			t.Errorf("形の違う番号を通した: %q", bad)
		}
	}
	if !validCode("00123456") {
		t.Error("先頭のゼロを通さない")
	}
}

func TestWaitApproveDeny(t *testing.T) {
	var w waitList
	for _, out := range []string{outApproved, outDenied} {
		p := newPending("a")
		if err := w.add(p); err != nil {
			t.Fatal(err)
		}
		got := make(chan string)
		go func() { got <- w.wait(p, time.Minute, nil, nil) }()
		if q, ok := w.look(p.code); !ok || q.id != "a" {
			t.Fatal("番号で見られない")
		}
		if id, ok := w.decide(p.code, out); !ok || id != "a" {
			t.Fatal("番号で決められない")
		}
		if r := <-got; r != out {
			t.Fatalf("結果 %q、期待 %q", r, out)
		}
		// 1回限り：同じ番号はもう使えない。
		if _, ok := w.decide(p.code, outApproved); ok {
			t.Fatal("同じ番号で2回決められた")
		}
		w.misses = 0
	}
}

func TestWaitTimeoutGoneStop(t *testing.T) {
	var w waitList
	p := newPending("t")
	w.add(p)
	if r := w.wait(p, 20*time.Millisecond, nil, nil); r != outTimeout {
		t.Fatalf("時間切れにならない: %q", r)
	}
	if _, ok := w.look(p.code); ok {
		t.Fatal("時間切れの後も待ちに残っている")
	}
	w.misses = 0
	for _, want := range []string{outGone, outStopped} {
		p := newPending("g")
		w.add(p)
		ch := make(chan struct{})
		close(ch)
		var r string
		if want == outGone {
			r = w.wait(p, time.Minute, ch, nil)
		} else {
			r = w.wait(p, time.Minute, nil, ch)
		}
		if r != want {
			t.Fatalf("結果 %q、期待 %q", r, want)
		}
	}
}

func TestWaitMisses(t *testing.T) {
	var w waitList
	ps := []*pending{newPending("1"), newPending("2")}
	for _, p := range ps {
		w.add(p)
	}
	wrong := "99999999"
	for _, p := range ps {
		if p.code == wrong {
			t.Skip("偶然一致した")
		}
	}
	w.look(wrong)
	w.decide(wrong, outApproved)
	if len(w.items) != 2 {
		t.Fatal("外れ2回で断ってしまった")
	}
	w.decide(wrong, outApproved)
	if len(w.items) != 0 {
		t.Fatal("外れ3回で待ちが残っている")
	}
	for _, p := range ps {
		if r := w.wait(p, time.Minute, nil, nil); r != outMisses {
			t.Fatalf("結果 %q", r)
		}
	}
	if w.misses != 0 {
		t.Fatal("外れの数を数え直していない")
	}
	// 正しい番号が使われたら数え直す。
	p := newPending("3")
	w.add(p)
	w.look(wrong)
	w.look(wrong)
	w.look(p.code)
	w.look(wrong)
	if len(w.items) != 1 {
		t.Fatal("正しい番号の後も外れが溜まっている")
	}
}

func TestWaitMax(t *testing.T) {
	var w waitList
	for i := 0; i < maxPending; i++ {
		if err := w.add(newPending("x")); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.add(newPending("y")); err != errTooMany {
		t.Fatalf("上限を超えて待ちに入った: %v", err)
	}
}

func TestPrintShown(t *testing.T) {
	var b strings.Builder
	printShown(&b, shown{ID: "abc", Agent: "claude\x1b[2J", Secret: "MONBAN_TEST", Command: []string{"/usr/bin/curl", "a\nb", "‮evil"}, User: true, Left: -3})
	out := b.String()
	for _, bad := range []string{"\x1b", "a\nb", "‮"} {
		if strings.Contains(out, bad) {
			t.Errorf("制御文字をそのまま出した: %q", bad)
		}
	}
	for _, want := range []string{`"claude\x1b[2J"`, `"a\nb"`, "注意", "残り 0秒"} {
		if !strings.Contains(out, want) {
			t.Errorf("%q が出ていない:\n%s", want, out)
		}
	}
}

func TestPushover(t *testing.T) {
	p := newPending("abc")
	p.code = "00123456"
	var form map[string][]string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		form = r.PostForm
		w.Write([]byte(`{"status":1}`))
	}))
	defer ts.Close()
	po := &pushover{token: "TOK", user: "USR", endpoint: ts.URL, client: ts.Client()}
	if err := po.send(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	msg := form["message"][0]
	if form["ttl"][0] != "120" || form["token"][0] != "TOK" || !strings.Contains(msg, "00123456") || !strings.Contains(msg, "引数 3") {
		t.Fatalf("送った中身が違う: %v", form)
	}
	// 引数の全文は外へ送らない。
	if strings.Contains(msg, "Authorization") || strings.Contains(msg, "api.example.com") {
		t.Fatalf("引数を送った: %q", msg)
	}
	// 起動と停止の知らせは、連鎖の写しとして残すので ttl を付けない。
	if err := po.note(context.Background(), "monban 起動", "ログ seq 1"); err != nil {
		t.Fatal(err)
	}
	if _, ok := form["ttl"]; ok || form["message"][0] != "ログ seq 1" {
		t.Fatalf("起動の知らせ: %v", form)
	}
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		w.Write([]byte(`{"status":0}`))
	}))
	defer bad.Close()
	po.endpoint, po.client = bad.URL, bad.Client()
	if err := po.send(context.Background(), p); err == nil || strings.Contains(err.Error(), "TOK") {
		t.Fatalf("失敗を通したか、トークンを出した: %v", err)
	}
}

func TestApprovalField(t *testing.T) {
	rule := func(r string) string {
		return `{"secrets":[{"name":"A","id":"00000000-0000-0000-0000-000000000000","allow":[` + r + `]}]}`
	}
	for _, c := range []struct {
		r    string
		ok   bool
		need bool
	}{
		{`{"command":"/usr/bin/printenv","agents":["*"]}`, true, true},
		{`{"command":"/usr/bin/printenv","agents":["*"],"approval":"always"}`, true, true},
		{`{"command":"/usr/bin/printenv","agents":["*"],"approval":"none"}`, true, false},
		{`{"command":"/usr/bin/printenv","agents":["*"],"approval":"window"}`, false, false},
		{`{"command":"/usr/bin/printenv","agents":["*"],"approval":"Always"}`, false, false},
	} {
		a, err := parseAllowlist([]byte(rule(c.r)))
		if (err == nil) != c.ok {
			t.Errorf("%s: %v", c.r, err)
			continue
		}
		if err == nil && a.secrets["A"].Allow[0].needsApproval() != c.need {
			t.Errorf("%s: 承認の要り方が違う", c.r)
		}
	}
}
