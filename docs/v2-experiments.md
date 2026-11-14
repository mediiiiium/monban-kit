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

## 実験1：Touch ID で署名させる（2026-11-14、人が指を当て、エージェントが記録した）

実験0で残した試験用の鍵（`.privateKeyUsage` ＋ `.biometryCurrentSet`）を使い、Release ビルドから試験用の文（`monban-exp1 test message 1` など）に署名させた。本物の秘密は使っていない。試し方は2つ：

- fresh：署名のたびに、新しい `LAContext`（理由の文つき）で鍵を探し直す
- same：一度探した鍵の参照で、続けて2回署名する

| 試したこと | 結果 |
|---|---|
| fresh で2回署名 | 2回とも Touch ID の画面が出て、指で通った（2.8秒・2.2秒）。署名は 70〜72 バイト、先頭 `30`（X9.62 の DER）、公開鍵で検証できた |
| same で2回署名 | 1回目は指で通った（1.8秒）。**2回目は画面が出ず、0.0秒で署名できた**（認証の使い回し） |
| 画面の中身 | 「Exp0で次の操作を実行しようとしています: monban 実験1：1回目の署名」「Exp0で続けるには、Touch ID を使用してください。」。ボタンは「キャンセル」だけで、**パスワードで通る道は出なかった** |
| 画面でキャンセル | 署名は失敗。`com.apple.LocalAuthentication -2`（利用者のキャンセル）。2回目も同じ |

読み：

- 指なしでは署名できない（Muse の言う第1層）は、鍵のアクセス制御で担保されている。画面にパスワードへの逃げ道はなく、キャンセルすれば失敗が返る
- **ただし、同じ鍵の参照を使い回すと、2回目以降は指なしで署名できる**。ヘルパーは承認のたびに新しい `LAContext` で鍵を探し直し、署名が済んだら `invalidate()` して、鍵の参照を持ち越さない作りにする。これは設計に書く決まりにする
- 画面に出る理由の文は、頼んだ側が渡した文がそのまま載る。照合コード（ヘルパーの表示と同じ数文字）を載せられる。アプリ名（ここでは「Exp0」）も出るので、本番ではヘルパーの名前を分かりやすくする
- 署名は DER 形式なので、門番（Go）では `ecdsa.VerifyASN1` で確かめられる見込み（実験4で固定する）
- 確かめていないこと：`invalidate()` で使い回しが本当に止まるか。同じ uid の別のプログラムが、ヘルパーが持っている認証済みの参照を使えるか（実験3・5に関わる）

## 実験3：同じ uid の別のプログラムから、その鍵で署名を頼めるか（2026-11-14、ビルドと探すのはエージェント、指は人）

実験0で残した試験用の鍵（アクセスグループ `<チームID>.io.github.mediiiiium.monban.exp0`）を、ヘルパー役（Exp0）ではないプログラムから使えるかを見た。本物の秘密は使っていない。

| 試したこと | 結果 |
|---|---|
| ad-hoc 署名の素のプログラム（包みなし・entitlements なし）で、タグを指定して探す（データ保護キーチェーン） | 見つからない（`-25300`） |
| 同じく、従来のキーチェーンで探す | 見つからない（`-25300`） |
| 同じく、タグなしで Secure Enclave の鍵を全部探す | 見つからない（`-25300`） |
| 別のアプリ：bundle ID を `io.github.mediiiiium.monban.exp3other` に変え、entitlements でヘルパーのアクセスグループを名乗って、同じ Personal Team で署名 | ビルドできた。Xcode が新しい App ID と profile を自動で作った。署名の entitlements にヘルパーのアクセスグループが入った |
| その別のアプリで探す | **見つかった（`0`）** |
| その別のアプリで署名させる | **指を当てると署名できた**（71 バイト、DER、検証 true）。キャンセルすると `-2` で失敗 |
| 画面の中身 | 「Exp0で次の操作を実行しようとしています: monban 実験3：別のアプリ（exp3other）から1回目の署名」。理由の文は別のアプリが渡したもの、アプリ名は別のアプリ自身の名前（ここではヘルパー役と同じ「Exp0」にしてあった）。見た目はヘルパーの画面と区別できない。2回目の画面も同じ形（「2回目の署名」）。どちらもボタンは「キャンセル」だけで、パスワードで通る道は出なかった |
| キーチェーンの許可を求める画面 | 人のスクリーンショットは Touch ID の画面2枚（1回目・2回目）だけで、ほかの画面が出たという報告はなかった |

なぜ名乗れたか：

- Personal Team の provisioning profile は、アクセスグループを `<チームID>.*`（同じチームならどれでも）で認可していた。同じチームで署名したアプリは、どれでもヘルパーのグループを名乗れる
- 別のアプリのビルド・署名・App ID と profile の発行は、**エージェントがシェルから `xcodebuild -allowProvisioningUpdates` で行い、人への確認の画面は一度も出なかった**。署名の身元（ログインキーチェーンにある Apple Development の証明書と秘密鍵）と、Xcode に入っている Apple ID の資格は、同じ uid のエージェントから使える

読み：

- 署名の身元を持たない素のプログラムからは、鍵は見えない。ここまでは OS が守る
- しかし、エージェントは同じ uid で署名の身元を使えるので、「ヘルパーのグループを名乗るアプリ」を自分で作れる。つまり**いまの形では、鍵はヘルパーだけに結び付いていない**。回答の言う第2層（画面に出た中身への承認）は成り立たない
- 第1層（指なしでは署名できない）は残る。偽のアプリでも、指を当てなければ署名は作られない。ただし偽のアプリは理由の文もアプリ名も自由に決められるので、人が画面を読んで見分けることはできない
- 回答の 4-4 のとおり、これで案 B は捨てない。格を落とした形（第1層だけ）として設計を書き直すか、結び付きを取り戻す手を探すかを決める
- 有料の Developer Program に替えても、署名の身元が同じ uid のログインキーチェーンにある限り、同じことが起きるはず（未確認）。効きそうなのは「身元をエージェントの手の届かないところに置く」ほうで、たとえばビルドと署名を別のユーザーで行う、ビルド後に身元をログインキーチェーンから外す、など。どれも未確認
- Touch ID の画面のほかに許可の画面は出なかったので、「許可画面で人が『常に許可』を押して崩れる」形ではなく、そもそも許可を求められずに通る形だった

後始末：

- 別のアプリ（`exp3other`）の App ID と profile が Personal Team に残っている。Xcode の profile は期限（7日）で切れる。App ID は無料の Apple ID では消せないことがあるので、残ったままでよい
- 試験用の鍵は、実験0の期限の確かめのために残したまま

## 実験3c：署名の身元をエージェントの手の届かない所に置くと、偽のアプリを作れなくなるか（2026-11-14、外すのは人、試すのはエージェント）

実験3で偽のアプリを作れた原因は、署名の身元が同じ uid から使えることだった。そこで、エージェントが署名の身元を手に入れる道を2つとも断った。

- ログインキーチェーンにある身元（Apple Development の証明書と秘密鍵）
- Xcode に入っている Apple ID（身元がなくても、`-allowProvisioningUpdates` で証明書を発行し直せる）

外すときに分かったこと：

- キーチェーンアクセスの「削除」では消えなかった。人が `security delete-identity -c "Apple Development"` を打って消した
- **Xcode を開いたまま、Apple ID も入ったままだと、消した身元はすぐに戻ってきた**。戻った証明書の発行時刻は同じ日の 9:10（日本時間）で、消す前のスクリーンショットと有効期限が同じだったので、同じ身元が入れ直されたように見える。どこから戻したかは確かめていない
- Xcode の「設定 > Apple Accounts」で Sign Out し、Xcode を終了してから消すと、身元は 0 件のままになった

断ったあとに試したこと：

| 試したこと | 結果 |
|---|---|
| 新しい bundle ID（`exp3c`）で、ヘルパーのアクセスグループを名乗るアプリを `xcodebuild -allowProvisioningUpdates` でビルド | 失敗。「No Accounts」と「No profiles for … were found」 |
| 実験3と同じ bundle ID（`exp3other`）で、もう一度ビルド（手元に profile は残っている） | 失敗。「No Accounts」と「No signing certificate "Mac Development" found … with a private key」 |
| 実験3で作った署名済みのアプリを、entitlements と profile を残したまま ad-hoc で署名し直して起動 | 起動で殺された（終了コード 137）。実験0の ad-hoc の偽物と同じ |
| ほかの資格（App Store Connect の API キー、`.p12` など）がホームにあるか | 見つからなかった（深さ4まで探した） |

読み：

- 身元と Apple ID の**両方**を断てば、エージェントは新しい偽のアプリを作れなかった。片方だけでは足りない。身元だけ消しても、Apple ID の入った Xcode があると戻ってくる
- ただし、**すでに署名済みのアプリは止まらない**。実験3の `exp3other` は、profile が切れるまで（7日）ヘルパーの鍵を使える。身元を外す前に作られた偽のアプリは、この手では防げない
- Personal Team の profile は 7 日で切れるので、本物のヘルパーは毎週作り直すことになる。作り直すたびに身元と Apple ID を戻すと、戻している間はエージェントも偽のアプリを作れる。実際に使うなら、ビルドと署名を別の macOS ユーザーで行い、エージェントの uid の側には身元も Apple ID も置かない形がよさそう（未確認）
- エージェントが Xcode を GUI で開いて Apple ID にサインインし直せるかは試していない。パスワードと2要素認証が要るはずだが、Mac 自体にサインインしている Apple アカウントでどこまで省かれるかは未確認
- 本物のヘルパーの差し替え（中身を書き換える、デバッガで乗っ取る）は、署名が壊れる・hardened runtime で止まるはずだが、この実験では試していない

後始末：

- 署名の身元と Xcode の Apple ID は外したまま。本物のヘルパーを作り直すときに戻す
- `exp3c` のプロジェクトとビルドはスクラッチパッドにあるだけで、リポジトリには入れていない

## 実験3d：ビルドと署名を別の macOS ユーザーで行うと、穴を開けずに作り直せるか（2026-11-14、ユーザーを作ってビルドするのは人、試すのはエージェント）

3c の形では、毎週の作り直しのたびに身元を戻す必要があり、戻している間は穴が開く。そこで、ビルドと署名だけをするユーザー（`monban-build`、管理者でない通常のユーザー）を人が作り、身元と Apple ID をそちらにだけ置いた。エージェントの uid の側には、身元も Apple ID も置かないままにした。

手順：

1. エージェントが実験0のソースを `/Users/Shared/monban-exp3d/` に置き、`main.swift` の SHA-256 を控えた
2. 人が `monban-build` でログインし、Xcode に同じ Apple ID でサインインした
3. 人が `monban-build` のターミナルでソースを自分のホームにコピーし、SHA-256 が控えと同じことを確かめてから、`xcodebuild -allowProvisioningUpdates` でビルドした。新しい Apple Development の証明書と、新しい profile（作成 11-14 10:08、期限 11-21 10:08）が `monban-build` の側に作られた
4. エージェントが、できたアプリを自分の側にコピーして試した

| 試したこと | 結果 |
|---|---|
| エージェントの uid の署名の身元 | 0 件 |
| エージェントから `xcodebuild -allowProvisioningUpdates` | 失敗。「No Accounts」と「No profiles for … were found」 |
| エージェントから `monban-build` の `~/Library/Keychains` と profile の置き場 | どちらも開けない（Permission denied） |
| `monban-build` が作ったアプリの署名 | `codesign --verify --strict` が通る。証明書は前と別、チームは同じ |
| そのアプリから、実験0で作った試験用の鍵を探す（`--find`） | 見つかった（0） |
| そのアプリで、その鍵を使って署名（`--sign fresh`、人が指を当てた） | 2回とも成功、検証も通った（5.3秒、1.7秒） |

途中で分かったこと：

- 人が最初に、間違えていつものユーザーの Xcode でサインインしかけた。そのあと、消してあった身元がいつものユーザーのキーチェーンに戻っていた（発行時刻は 9:23（日本時間））。Sign Out・Xcode の終了・`security delete-identity` をやり直して 0 件に戻した。サインインする画面がどのユーザーのものかは見た目で分かりにくく、運用ではここが間違えやすい
- 共有フォルダの `monban-exp3d` はエージェントが作ったので、`monban-build` は中に書けなかった（`mkdir: Permission denied`）。代わりに、`monban-build` のホームの `exp3d` がほかのユーザーから読める（書けない）ので、エージェントはそこから直接コピーした
- この Mac は SSH（リモートログイン）が開いているが、入れるのは admin グループだけ。`monban-build` は通常のユーザーなので、SSH では入れない（グループの設定を読んだだけで、実際に入れないかは試していない）

読み：

- **署名の証明書が別のユーザーの新しいものに変わっても、チームとアクセスグループが同じなら、前に作った鍵をそのまま使えた。** 鍵は証明書ではなく、チームとアクセスグループに結び付いている。毎週の作り直しを `monban-build` で行えば、エージェントの側に身元を戻す時間がなくなる
- 新しい profile で作り直しても鍵が見えることは、11-21 に試す予定だった2つ目（作り直し）を、期限が切れる前に `monban-build` で先に確かめた形になる。期限が切れた後の作り直しは、11-21 以降にもう一度 `monban-build` で試す
- **新しい穴：ソースのすり替え。** `monban-build` がビルドするソースは、エージェントが書き込める場所（共有フォルダ、作業中のリポジトリ）から来る。身元を隔離しても、エージェントがソースを書き換えれば、偽の中身に正規の署名が付く。今回は SHA-256 を控えと比べたが、控えを出したのもエージェントなので、守りになっていない。運用するなら、作り直しの前に人が差分を読む、または人が確かめたコミットだけをビルドする決まりが要る
- すでに署名済みの偽のアプリ（実験3の `exp3other`、11-20 まで）が止まらないことは変わらない
- `monban-build` の作業フォルダは、ほかのユーザーから読める。書けないので身元は漏れないが、本人だけが開けるようにしてもよい

後始末：

- `monban-build` のユーザー、その Xcode の Apple ID、その身元は残す。毎週の作り直しと、11-21 以降の試しに使う
- エージェントの uid の側の身元は 0 件、Xcode の Apple ID も入っていない
- `exp3d` のアプリはスクラッチパッドにあるだけで、リポジトリには入れていない。試験用の鍵は 11-21 以降の試しのために残す

## 残り

- profile の期限が切れた後（2026-11-21 以降）に、実験0の2つを試す。1つ目（古いアプリ `Exp0-profile1120.app` が鍵を使えるか）はエージェントの側でそのまま試す。2つ目（作り直し）は `monban-build` でビルドし直して試す。エージェントの側に身元は戻さない
- 実験2（`biometryCurrentSet` の鍵）は、実験0・1で同じフラグの鍵を作って署名できたので、ほぼ済み。署名の種類ごとの記録は、Personal Team で作れて ad-hoc で作れない（`-34018`）まで取れている
- 実験3c・3d を受けて、第2層ありの形（ビルドは `monban-build`、作り直しの前に人が差分を読む）で進めると決め、design.md の「門番の v2」に書いた（2026-11-14）。次は実験4
