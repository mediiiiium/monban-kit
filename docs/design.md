# AIエージェントも使える、自分用のパスワード管理の構成をOSSで出す

作成日: 2026-11-10

## ステータス
- 2026-11-10 に ideas の seeds から移した。まだコードはない
- 形：**構成集**（部品の組み合わせ＋導入手順＋足りない分だけの薄いコード）。dotfiles を公開する感覚。パスワードを預かるサービスではない
- 2026-11-10 に Muse と壁打ちした。「新しいデーモンより構成集」で一致
- 2026-11-10 に名前を **monban-kit** に決めた（仮名 agent-password-setup から変更）。理由：使うのはエージェントだけではない（本人の複数デバイス、スクリプト、CI も門番が仲介する）ので、名前に agent を入れると実態より狭くなる。kit は「部品の組み合わせ＋手順」という中身と一致。monban-kit の完全一致は検索で見当たらず。リポジトリ作成後（2026-11-10）に確認：GitHub は自分の mediiiiium/monban-kit だけ、npm の monban-kit は未登録。npm の `monban`（単体）は別の認証ライブラリが使っている。GitHub の flythenimbus/monban（macOS・Linux のセキュリティキー暗号化、スター4）は近い領域なので、公開時の説明で混同されないようにする

---

## 一次経験（ここが出発点）

- パスワード管理が面倒。AIエージェントにも使わせたいが、渡し方が毎回手作業で怖い
- 複数のデバイス（Mac・Linux・Windows・スマホ）で使いたい
- AIは Claude に限らない（Codex、Gemini CLI など）

## 要件
- 金庫はクラウドでよい → Bitwarden（複数デバイス、エージェント向けの部品が一番揃っている）
- 原則：エージェントには必要な項目だけ、人の承認を経て渡す。「見せない」は効果を盛らずに書く

---

## 構成（見直し後）

```
人用の金庫      : Bitwarden 個人金庫（普段使い。エージェントは触らない）
エージェント用  : Bitwarden Secrets Manager（無料：マシンアカウント3・プロジェクト3・秘密は無制限）
                  ログイン情報は値にJSONを入れる規約で持つ（Notte の BitwardenVault の前例）
門番            : 許可リスト・代理実行・時間枠つき承認・キルスイッチ（mcp-warden の設計を借りる）
離れた場所      : Bitwarden Agent Access（ログインごとに人が承認。中継サーバーはセルフホスト推奨）
```

**途中で捨てた案と理由**

| 案 | 捨てた理由 |
|---|---|
| `bw serve` を常駐させ、その前に門番を置く | `bw serve` には認証がない。同じユーザーのエージェントは門番を通らずに全件取れる。別ユーザー＋Unixソケット（`--hostname unix:///パス`、CLI 2025.10.0〜。コードで確認）にすれば防げるが、Mac・Windows の導入が重い |
| `rbw` で高速化 | エージェントが `rbw get` を直接呼べるので門番を迂回される。Windows に触れていない。パスキーとDuoの2段階認証に対応していない |
| 公式 Bitwarden MCP をそのまま使う | 呼び出しごとに `bw` を起動するので遅い（`bw status` で約3.4秒）。パスワードをAIに返す。項目を絞る設定がない |

---

## 構成集に書くこと

- **2つのモード**：簡易＝同じユーザー＋門番＋承認（隔離ではなく事故防止だと明記）／厳格＝専用ユーザーかVM。Windows は WSL2 に寄せて Linux の手順に一本化する
- **承認**：
  - リスクで段を分ける。低リスクは時間枠つきの事前承認、高リスクは毎回の強い承認（Touch ID・Windows Hello・スマホ通知）
  - 承認画面には、エージェントの説明文ではなく門番が作った事実（項目・操作・宛先・コマンドのハッシュ）を出す
  - 承認はそのリクエストだけに使える1回限り・短命なものにする
- **「見せない」の段**：
  - A：承認後に値を返す
  - B：環境変数で注入する（モデルの文脈に残らないだけで、秘匿ではない）
  - C：代理実行（高価値の少数だけ）
  - ワンタイムコードは、シードではなく現在のコードだけを返す
- **無料枠での配分**（2026-11-10、公式の比較表で確認）：
  - 無料の上限は、マシンアカウント3つ・プロジェクト3つ・ユーザー2人。秘密の数は無制限
  - マシンアカウントは**1台に1つ**にする。エージェントごと（Claude Code・Codex・Gemini CLI・CI）に作ると、3つでは足りない。同じ台の中でどのエージェントかを見分けるのは、門番の許可リストとログで行う
  - 条件：合言葉（アクセストークン）は門番だけが持ち、エージェントには渡さない。エージェントが合言葉を読めると、門番を通らずに、そのマシンアカウントで読める秘密を全部取れる
  - イベントログと監査ログは有料（チーム、$6/ユーザー/月）からの機能。無料のまま使うなら、誰がいつ何を読んだかの記録は門番の側で持つ
- **失効手順**：パスワード変更・aac のペアリング解除・停止を1手順にまとめる
- **サプライチェーン**：中継サーバーのセルフホスト手順、bw と aac のチェックサム確認
- **門番の2系統**：個人金庫が背後＝otscup/secrets-broker／Secrets Manager が背後＝R055LE/secrets-broker（Linuxのみ）。otscup はライセンス表記が AGPL と MIT で食い違うので、同梱・改変するなら作者に確認する

---

## 競合（2026-11-10 調査）

**部品は揃っている。空いているのは「Bitwarden を背後にした個人の構成＋導入手順」。**

- [otscup/secrets-broker](https://github.com/otscup/secrets-broker)：Bitwarden互換の金庫（bw CLI）を背後に置き、エージェントごとの合言葉、承認（Telegram・ntfy・Web、時間枠つき）、MCP、代理実行まで揃っている。一番近い。スター0、リリースなし
- [ermoluk/mcp-warden](https://github.com/ermoluk/mcp-warden)：一番成熟している（Apache-2.0、v0.6.0）。許可リスト、門番側での注入、時間枠つき承認、キルスイッチ、Telegram承認。金庫はOSのキーチェーンで、**Bitwardenには対応していない**
- [R055LE/secrets-broker](https://github.com/R055LE/secrets-broker)：Secrets Manager を背後に置く。別のLinuxユーザー＋別デバイスで承認する。Linuxのみ
- [asteby/veil](https://github.com/asteby/veil)：値をエージェントに見せずに人が入力するMCP。Bitwardenには対応していない。初期段階
- [OneCLI](https://onecli.sh/blog/bitwarden-agent-access-sdk-onecli)：Agent Access と連携し、通信の段階で認証情報を注入するゲートウェイ。アルファ版
- [Notte](https://docs.notte.cc/integrations/bitwarden)：AacVault（Agent Access 経由で個人金庫を読むだけ。ログインごとに `aac listen` 側で承認）と BitwardenVault（Secrets Manager。ログイン1件を秘密1つにし、値はJSON。`url` と `password` が必須、`username`・`email`・`mfa_secret`（TOTPシードか otpauth URI）は任意）。どちらも手元のセッション専用（[PR #816](https://github.com/nottelabs/notte/pull/816)）
- Bitwarden 公式：[MCPサーバー](https://github.com/bitwarden/mcp-server)、[Agent Access](https://github.com/bitwarden/agent-access)（Apache-2.0、初期プレビュー、v0.11.0）
- 1Password の日本語記事、Passwd、Cerberus KeyRouter、warden-mcp も同じ領域

---

## 門番の v1（2026-11-10 に Muse と相談して決めた）

相談と回答：[muse-consult-gatekeeper.md](muse-consult-gatekeeper.md)、[muse-consult-gatekeeper-response.md](muse-consult-gatekeeper-response.md)

- **形**：薄い門番を自分で書く（案 B）。mcp-warden からは許可リストの書き方・時間枠つき承認・ハッシュ連鎖のログを、R055LE/secrets-broker からは別デバイス承認の段取りを借りる。コードは借りない
- **作り**：
  - Go の単一バイナリ。Mac の別ユーザー `_monban` で launchd のデーモンとして動かす。エージェントとは Unix ソケットだけで話す
  - 合言葉は `_monban` のキーチェーンに置き、`bws` には環境変数で渡す（引数では渡さない）
  - `bws` は子プロセスで呼ぶ。起動時に `bws` の署名（TeamIdentifier `LTZ2PFU5D6`）を確かめる
- **v1 に入れる**：読むだけ、許可リスト、ログ（`_monban` の持ち物にして、エージェントから書き換えられなくする）、キルスイッチ、コマンド単位の注入（`monban run`）、スマホへの承認依頼（Pushover）
- **v1 に入れない**：MCP、代理実行（段 C）、Touch ID のヘルパー（v2。LaunchAgent でログイン中のユーザーとして動かし、合言葉は持たせない）、Windows、ログイン情報の JSON 規約、**高リスクの秘密**（強い承認が付くまで門番に預けない）
- **v1.5 の候補**：注入先のコマンドの出力から秘密の値を探して伏せる
- **正直に書くこと**：
  - 許可リストのエージェント欄は名札で、認証ではない（Claude Code も Codex も同じ uid）。守りの実体は uid の分離と承認
  - ターミナルの承認コマンドは、同じユーザーのエージェントが真似できる。事故防止であって、高リスクには使わない
  - キルスイッチは即時遮断ではない。門番を止めて合言葉を失効させても、最大1時間は読める
- **変えない決まり**：マシンアカウントは Can read のまま。門番が乗っ取られても、秘密の書き換えや削除はできない
- **逃げ道**：別ユーザーで足りない高価値の秘密が出たら、Linux VM の中に門番を置く（R055LE がそのまま動く）

## 次の一手（未決）

- Bitwarden の無料版と Secrets Manager の無料版を1週間実際に使ってみる
- mcp-warden に Bitwarden（Secrets Manager）対応を足す提案ができるか、作者の方針を見る
- 1本だけ手で通す：Secrets Manager の秘密を1つ、承認つきで Claude Code と Codex の両方から使えるか

## 未確認
- otscup/secrets-broker が bitwarden.com の個人クラウドで実際に動くか
- 別ユーザーで動く門番から Touch ID の確認を出せるか → Muse の答え：門番自身は出せない。ログイン中のユーザーで動くヘルパー（LaunchAgent）を置けば出せる。まだ試していない
- 別ユーザーのプロセスの環境変数を、ほかのユーザーが `ps eww` で読めないか → 2026-11-10 に `nobody` の `sleep` で確かめた。root でなければ読めない（root からは読める）。本番の形（launchd・`_monban`・`bws`）ではまだ。結果：[pre-v1-experiments.md](pre-v1-experiments.md) の実験1
- `bws` の1回の呼び出しにかかる時間 → 2026-11-10 に測った。温まった `get` は約0.5秒、状態ファイルがない初回は約1.1秒。v1 は毎回子プロセスで呼ぶ形のままでよい。結果：同じファイルの実験2
- Agent Access の既定の中継サーバー（ap.lesspassword.dev）を誰が運営しているか
