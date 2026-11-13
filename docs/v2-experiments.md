# v2 の実験（Touch ID の承認）

作成日: 2026-11-13
相談と回答：[muse-consult-v2-touchid.md](muse-consult-v2-touchid.md)、[muse-consult-v2-touchid-response.md](muse-consult-v2-touchid-response.md)

回答の 4-4 の順に、実験0〜6を行う。結果はここに足していく。

## 決まり

- 合言葉や本物のパスワードは、このリポジトリにも会話にも出さない。試すときの鍵には試験用の名前を付け、終わったら消す
- 「見せない」の効果を盛らない
- sudo はエージェントに打たせない

## 手元の道具（2026-11-13）

- Command Line Tools だけ（Xcode はない）。swiftc 6.3.3（arm64-apple-macosx26.0）
- コード署名の身元は0件（`security find-identity -v -p codesigning`）。いま試せる署名は ad-hoc だけ

## 実験0：包みと署名

### ad-hoc 署名・包みなし（2026-11-13、エージェントが行った）

最小の Swift のプログラム（scratchpad）を `codesign -s - -o runtime` で署名した（`Signature=adhoc`、`flags=adhoc,runtime`、`TeamIdentifier=not set`）。アクセス制御は `privateKeyUsage` と `biometryCurrentSet`、`kSecAttrAccessibleWhenUnlockedThisDeviceOnly`。ターミナルから直接動かした（LaunchAgent ではない）。

| 鍵の作り方 | 結果 |
|---|---|
| その場限り（`kSecAttrIsPermanent = false`） | 作れた。公開鍵は 65 バイト（先頭 `04`、X9.63 の生形式） |
| その場限り＋データ保護キーチェーンを指定 | 作れた（同上） |
| キーチェーンに残す（`kSecAttrIsPermanent = true`）＋データ保護キーチェーン | 作れない。`-34018`（`errSecMissingEntitlement`）、`failed to add key to keychain` |

読み：

- ad-hoc では、Secure Enclave の鍵をキーチェーンに残せない。回答の見立て（アクセスグループの entitlement が要り、ad-hoc では主張できない）のとおり
- その場限りの鍵は作れる。ただしプロセスが終わると消えるので、ヘルパーを起動し直すたび（再起動のたび）に登録の儀式が要る。結び付きは「そのプロセスのメモリ」で、強いが運用に向かない
- 公開鍵の形式は X9.63 の生形式（65 バイト、`04` で始まる）。実験4で Go 側の読み方を決めるときの材料

### 残り

- Personal Team の署名（Xcode と Apple ID が要る）
- アプリ風の包み（provisioning profile を埋め込む）
- LaunchAgent で動かす
