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

### Release ビルド・LaunchAgent・作り直し（2026-11-14、エージェントが行った）

プロジェクトに Release の設定を足した。中身は Debug と同じで、`CODE_SIGN_INJECT_BASE_ENTITLEMENTS = NO` だけを加えた（Xcode が `get-task-allow` を足さないようにするため）。LaunchAgent は試験用のラベル（`io.github.mediiiiium.monban.exp0test`）で `launchctl bootstrap gui/<uid>` に入れ、`RunAtLoad` で1回だけ動かし、終わったら `bootout` した。sudo は使っていない。

| 試したこと | 結果 |
|---|---|
| Release ビルドの entitlements | `application-identifier`、`team-identifier`、`keychain-access-groups` の3つだけ。`get-task-allow` はない。`embedded.provisionprofile` は入っている |
| Release ビルドで作って残す → 別プロセスで探す | 作れた（65 バイト、`04`）、見つかった（`0`）。Debug ビルドからも見えた |
| LaunchAgent から探す（ターミナルで作った鍵） | 見つかった（`0`、終了コード 0） |
| LaunchAgent で消して作り直し、取り出す | できた。アクセスグループもトークンも同じ |
| その鍵をターミナルから探す | 見つかった |
| ソースを1行変えてビルドし直し、探す | 見つかった。profile は同じもの（UUID が同じ）が使われた |

provisioning profile の期限：

- 作られたのは 2026-11-13、期限は 2026-11-20（7日）。このマシン1台だけに結び付いている
- 期限が切れた後のことはまだ確かめていない。期限が切れたら、次の2つを試す
  1. 期限前にビルドした .app（`~/monban-exp0/Exp0-profile1120.app`）は起動できるか。起動できるなら、鍵は見えるか
  2. ビルドし直して新しい profile が入った .app から、期限前に作った鍵は見えるか
- そのために、試験用の鍵（`monban exp0 test key`）は消さずに残してある。プロジェクトの写しも `~/monban-exp0/` に置いた（scratchpad は消えることがあるため）。期限の確かめが終わったら、鍵もこのフォルダも消す

読み：

- Release ビルドでも、LaunchAgent からでも、鍵は残せて使える。門番の本番の形（LaunchAgent で常駐）で困ることは、今のところ見つかっていない
- 鍵はバイナリのハッシュではなく、アクセスグループに結び付いている。そのため、作り直しても鍵は見える
- 残る心配は期限の1つだけ。7日ごとにビルドし直す必要があるなら、手順に入れるか、有料の Developer Program を考えるかを、そのときに決める

### 残り

- profile の期限が切れた後（2026-11-21 以降）に、上の2つを試す
- 実験0が片付いたら、実験1（Touch ID で署名させる）へ
