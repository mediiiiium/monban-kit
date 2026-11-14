package main

import (
	"crypto/elliptic"
	"crypto/sha256"
	"encoding/asn1"
	"encoding/hex"
	"math/big"
	"testing"
)

// ダミーの試験データ。Go のソフトウェアの鍵で作った（Secure Enclave の鍵ではない）。秘密の鍵は捨ててある。
// Secure Enclave が出した署名でも同じ形で通ることは、実験4で確かめた（docs/v2-experiments.md）。
const (
	touchIDDummyPub = "04269b692173b99900ce87dedee40213e738d6b97afb7eb7311239229957306800b8d815714c5788e0926ed57f5402ea8003f299fc8651e8d50512c5ec6b39cb14"
	touchIDDummyMsg = "monban-exp4 dummy vector"
	touchIDDummySig = "30440220428ab1dafd26e92e3998ede3469cef7c5be580697e535a4750f091145026a32202202c0dd47e00e039f0654ba133578a09fa0d84202b2ce165aee2a83db636728a72"
)

func touchIDDummy(t *testing.T) (pub, msg, sig []byte) {
	t.Helper()
	pub, err := hex.DecodeString(touchIDDummyPub)
	if err != nil {
		t.Fatal(err)
	}
	sig, err = hex.DecodeString(touchIDDummySig)
	if err != nil {
		t.Fatal(err)
	}
	return pub, []byte(touchIDDummyMsg), sig
}

func flip(b []byte, i int) []byte {
	c := append([]byte{}, b...)
	c[i] ^= 1
	return c
}

func TestVerifyTouchIDSignature(t *testing.T) {
	pub, msg, sig := touchIDDummy(t)
	if err := verifyTouchIDSignature(pub, msg, sig); err != nil {
		t.Fatalf("正しい署名が通らない: %v", err)
	}
	h := sha256.Sum256(msg)
	compressed := append([]byte{0x02 + pub[64]&1}, pub[1:33]...)
	bad := []struct {
		name          string
		pub, msg, sig []byte
	}{
		{"文を1ビット変える", pub, flip(msg, 0), sig},
		{"署名の最後を1ビット変える", pub, msg, flip(sig, len(sig)-1)},
		{"署名の後ろに1バイト足す", pub, msg, append(append([]byte{}, sig...), 0)},
		{"署名が空", pub, msg, nil},
		{"公開鍵が曲線に乗らない", flip(pub, 64), msg, sig},
		{"圧縮形式の公開鍵", compressed, msg, sig},
		{"先頭の 04 がない公開鍵", pub[1:], msg, sig},
		{"公開鍵が空", nil, msg, sig},
		{"ハッシュを文として渡す", pub, h[:], sig},
		{"文が空", pub, nil, sig},
	}
	for _, c := range bad {
		if err := verifyTouchIDSignature(c.pub, c.msg, c.sig); err == nil {
			t.Errorf("%s: 通ってしまった", c.name)
		}
	}
}

// s を n-s にした署名は通る（ECDSA の別解）。問いが1回限りなので受け入れる、と決めたことを書き留めておく。
func TestVerifyTouchIDSignatureAcceptsHighS(t *testing.T) {
	pub, msg, sig := touchIDDummy(t)
	var rs struct{ R, S *big.Int }
	if rest, err := asn1.Unmarshal(sig, &rs); err != nil || len(rest) != 0 {
		t.Fatalf("DER が読めない: %v", err)
	}
	rs.S.Sub(elliptic.P256().Params().N, rs.S)
	alt, err := asn1.Marshal(rs)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyTouchIDSignature(pub, msg, alt); err != nil {
		t.Fatalf("別解が通らない: %v", err)
	}
}
