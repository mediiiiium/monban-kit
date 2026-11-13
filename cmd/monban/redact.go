package main

// 出力から秘密の値を伏せる（v1.5）。run が子のコマンドの stdout・stderr をこれに通してから流す。
// 守ること：
// - 値と完全に一致するバイト列だけを伏せる。値を変えた形（base64 など）や、値の一部は伏せない
// - 書き込みの境目で値が割れても拾う。手元に残すのは「値の先頭と一致している末尾」だけで、
//   一致しないと分かった分はすぐ流す（待たせるのは、値の先頭と一致している間だけ）
// - 値の途中に別のバイトが挟まった出力（色付けの制御文字など）は、一致しないので伏せない
// 相談と回答：docs/muse-consult-redact-output.md、docs/muse-consult-redact-output-response.md

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
)

// redactMark は、伏せた値の代わりに出す文字列。先頭と末尾を全角の括弧にする。値は JSON の文字列から
// 来るので正しい UTF-8 で、正しい UTF-8 は続きのバイト（0x80〜0xBF）で始まれず、先頭のバイトで終われない。
// だから印との重なりは文字の単位になり、ASCII だけの値は印と重ならない（checkRedactable で断られない）。
const redactMark = "【monban: 伏せた】"

// runNoticePrefix は、子が終わった後に run が stderr に足す行の書き出し。子の stderr の最後に
// 手元に残した分（値の先頭の一部）が流れた直後に続くので、この書き出しとつながって値ができないよう、
// checkRedactable で末尾との重なりも見る。
const runNoticePrefix = "【monban run】"

// minValue は、伏せられる値の短さの下限（バイト）。これより短い値は、正常な出力の中にも
// ありふれて現れ、伏せると出力を静かに書き換えてしまうので、門番も run も渡さない・動かさない（checkRedactable）。
const minValue = 8

// checkRedactable は、値を出力から伏せられるかを確かめる。伏せられない値は、門番も渡さず、run も動かさない。
//   - minValue より短い値
//   - 置き換えの文字列（redactMark）と重なる値。値が redactMark を含む・redactMark に含まれる、
//     値の先頭が redactMark の末尾と一致する、値の末尾が redactMark の先頭と一致する、のどれか。
//     重なると、伏せた結果と前後の出力がつながって、値そのものができてしまうことがある
//     （値が "a"+redactMark+"b" で、出力が "a"+値+"b" なら、伏せた結果が値と同じになる）
//   - 値の末尾が runNoticePrefix の先頭と一致する値（run の知らせの行とつながる）
//
// 出力に新しく現れた値は、どこかで redactMark と重なっているはずなので（左から重ならない置き換えなので、
// 置き換えなかった区間に値がまるごと入ることはない）、この4つを断れば足りる。
func checkRedactable(value string) error {
	if len(value) < minValue {
		return fmt.Errorf("値が短すぎて出力から伏せられない（%dバイト未満）", minValue)
	}
	overlap := errors.New("値が伏せた印と重なるので出力から伏せられない")
	if strings.Contains(value, redactMark) || strings.Contains(redactMark, value) {
		return overlap
	}
	for k := 1; k < len(redactMark) && k <= len(value); k++ {
		if strings.HasPrefix(value, redactMark[len(redactMark)-k:]) || strings.HasSuffix(value, redactMark[:k]) {
			return overlap
		}
	}
	for k := 1; k <= len(runNoticePrefix) && k <= len(value); k++ {
		if strings.HasSuffix(value, runNoticePrefix[:k]) {
			return overlap
		}
	}
	return nil
}

// redactor は、書き込まれたバイト列から値を伏せて w に流す。値の照合は KMP で1バイトずつ進める。
// 手元に残す分（値の先頭 j バイトと一致している末尾）は、値そのものなので、別に持たない。
type redactor struct {
	w     io.Writer
	value []byte
	fail  []int // fail[i]：value[:i+1] の、自分自身ではない最長の「先頭＝末尾」の長さ
	j     int   // いま一致している、値の先頭のバイト数
	n     int   // 伏せた数
	err   error
}

func newRedactor(w io.Writer, value string) *redactor {
	v := []byte(value)
	fail := make([]int, len(v))
	for i, k := 1, 0; i < len(v); i++ {
		for k > 0 && v[i] != v[k] {
			k = fail[k-1]
		}
		if v[i] == v[k] {
			k++
		}
		fail[i] = k
	}
	return &redactor{w: w, value: v, fail: fail}
}

// Write は、p を照らして、確定した分を w に1回で書く。p の長さを返す（手元に残した分も受け取った扱い）。
// w への書き込みが失敗したら、以後はそのエラーを返し続ける。
func (r *redactor) Write(p []byte) (int, error) {
	if r.err != nil {
		return 0, r.err
	}
	v := r.value
	if len(v) == 0 {
		// 空の値は伏せようがない（run は checkRedactable を通らない値で動かないので、ここには来ない）。
		return r.w.Write(p)
	}
	var out bytes.Buffer
	for _, c := range p {
		j := r.j
		for j > 0 && c != v[j] {
			j = r.fail[j-1]
		}
		if c == v[j] {
			j++
		}
		// 一致していた r.j バイト（＝v[:r.j]）に c を足した r.j+1 バイトのうち、末尾の j バイトが残る。
		// 先頭の r.j+1-j バイトは、値の一部ではないと決まったので流す。
		if drop := r.j + 1 - j; drop > 0 {
			if drop <= r.j {
				out.Write(v[:drop])
			} else {
				out.Write(v[:r.j])
				out.WriteByte(c)
			}
		}
		if j == len(v) {
			out.WriteString(redactMark)
			r.n++
			j = 0
		}
		r.j = j
	}
	if out.Len() > 0 {
		if _, err := r.w.Write(out.Bytes()); err != nil {
			r.err = err
			return 0, err
		}
	}
	return len(p), nil
}

// Flush は、手元に残した分（値の先頭の一部で、値にはならなかったもの）を流す。子が終わってから呼ぶ。
func (r *redactor) Flush() error {
	if r.err != nil || r.j == 0 {
		return r.err
	}
	_, r.err = r.w.Write(r.value[:r.j])
	r.j = 0
	return r.err
}
