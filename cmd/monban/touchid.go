package main

// Touch ID の承認（v2）で、ヘルパーが返した署名を確かめる。形式は実験4で固定した（docs/v2-experiments.md）。
// - 公開鍵：X9.63 の生形式（65 バイト、04‖X‖Y）。ヘルパーの SecKeyCopyExternalRepresentation がそのまま出す形
// - 署名：X9.62 の DER（SEQUENCE{r, s}）。ヘルパーは .ecdsaSignatureMessageX962SHA256 で署名する
// - 署名の対象：問いのバイト列そのもの。ハッシュ（SHA-256）は、ヘルパー側は Security の中で、門番側はここで取る
// 曲線は P-256 だけ。圧縮形式の公開鍵、曲線に乗らない点、DER の後ろの余りは、どれも断る。
// s を n-s にした署名（ECDSA の別解）は通る。問いは1回限りなので、同じ問いへの別の署名が作れても困らない。

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"errors"
	"fmt"
)

func verifyTouchIDSignature(pub, msg, sig []byte) error {
	k, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), pub)
	if err != nil {
		return fmt.Errorf("touchid: 公開鍵が読めない: %w", err)
	}
	h := sha256.Sum256(msg)
	if !ecdsa.VerifyASN1(k, h[:], sig) {
		return errors.New("touchid: 署名が合わない")
	}
	return nil
}
