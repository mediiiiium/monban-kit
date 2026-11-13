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

2026-11-14 に更新：

- Xcode 27.2（27B52）を入れ、無料の Apple ID で Personal Team にログインした（本人が行った）
- 最小のプロジェクトでチームを選ぶと、Xcode が「Apple Development」の証明書を1つ作った。身元は1件になった

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

### Personal Team 署名・アプリの包み（2026-11-14、エージェントが行った）

scratchpad に最小の macOS アプリのプロジェクトを手で書いた（`main.swift` だけ、`LSBackgroundOnly`、Hardened Runtime あり）。entitlements は `keychain-access-groups` に `$(AppIdentifierPrefix)io.github.mediiiiium.monban.exp0` を1つ。チームは本人が Xcode で選んだ。ビルドは `xcodebuild -allowProvisioningUpdates`。鍵のアクセス制御は ad-hoc のときと同じにした。ターミナルから `Exp0.app/Contents/MacOS/Exp0` を直接動かした（LaunchAgent ではない）。

署名の中身：

- `TeamIdentifier` は Personal Team の ID。`flags=runtime`
- entitlements は `com.apple.application-identifier`、`com.apple.developer.team-identifier`、`keychain-access-groups`（`<チームID>.io.github.mediiiiium.monban.exp0`）、`com.apple.security.get-task-allow = true`（Debug ビルドだから）
- `Contents/embedded.provisionprofile` が入っている（Xcode Managed Profile）

| 試したこと | 結果 |
|---|---|
| キーチェーンに残す（`kSecAttrIsPermanent = true`）＋データ保護キーチェーン | 作れた。公開鍵は 65 バイト（先頭 `04`） |
| 同じプロセスで取り出す | 取れた。アクセスグループは entitlements のもの、トークンは `com.apple.setoken` |
| 別のプロセス（正規の署名）で探す | 見つかった（`0`） |
| 偽物：同じ .app を ad-hoc で署名し直し、同じ entitlements を主張（profile は入ったまま） | 起動した途端に殺された（終了コード 137 = SIGKILL） |
| 偽物：ad-hoc で `keychain-access-groups` だけを主張 | 同じく殺された（137、`Operation not permitted`） |
| 偽物：ad-hoc、entitlements なし | 起動はするが、鍵は見つからない（`-25300`、`errSecItemNotFound`） |
| 後始末（正規の署名で消す） | 消えた（その後に探すと `-25300`） |

読み：

- Personal Team の署名と provisioning profile があれば、Secure Enclave の鍵をキーチェーンに残せる。ad-hoc のときの `-34018` はこれで解ける。有料の Developer Program は要らなかった
- 鍵はアクセスグループ（＝チーム ID と bundle ID）に結び付く。ad-hoc の偽物がそのアクセスグループを主張しても、OS が起動の時点で止める。主張しなければ、鍵は見えない
- 試験用の鍵（`monban exp0 test key`）は消した
- 作るときと探すときには、Touch ID は求められなかった。指が要るのは署名するときのはず（実験1以降で確かめる）
- 気になる点：Personal Team の provisioning profile には期限がある（無料の Apple ID では短いと言われる）。期限が切れたときに、鍵が読めなくなるのか、ビルドし直せば戻るのかは、まだ確かめていない
- Debug ビルドなので `get-task-allow = true`（デバッガが付けられる）。本番の形では外す必要がある

### 残り

- LaunchAgent で動かす
- profile の期限とビルドし直しの影響
- `get-task-allow` を外した形（Release ビルド）
