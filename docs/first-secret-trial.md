# Secrets Manager の秘密を1つ、手で通す（試用の手順）

作成日: 2026-11-10
目的：門番を書く前に、`bws` だけでどこまでできて、どこが足りないかを目で見る。Claude Code と Codex の両方で試す。
前提：Mac。Bitwarden の公式ヘルプと GitHub のリリースで確認した内容（出典は末尾）。

## 決まり

- 秘密の値は**ダミー**にする（例：`hello-monban`）。本物のパスワードは使わない
- 合言葉（アクセストークン）は、このリポジトリにも、AI との会話にも貼らない。Mac のキーチェーンにだけ置く
- 合言葉の期限は短くする（試用が終わる日まで）。終わったら失効させる

## 1. Web の画面で用意する

Secrets Manager に切り替えてから行う。無料版は、ユーザー2人・プロジェクト3つ・マシンアカウント3つまで。秘密の数に上限はない。

1. プロジェクトを作る：「New」→「Project」→ 名前は `monban-trial`
2. 秘密を作る：「New」→「Secret」
   - Name：`MONBAN_TEST`（この名前が、そのまま環境変数の名前になる）
   - Value：`hello-monban`
   - Project：`monban-trial`
3. マシンアカウントを作る：「New」→「Machine account」→ 名前は `mac-trial`
   - 開いて「Projects」タブで `monban-trial` を足す。権限は **Can read**（読むだけ）
4. 合言葉を作る：`mac-trial` の「Access tokens」タブ →「Create access token」
   - 「Expires」は既定が「Never」（期限なし）なので、必ず変える
   - 合言葉は**一度しか表示されない**。次の手順でそのままキーチェーンに入れる

## 2. `bws` を入れる

公式ヘルプは `curl https://bws.bitwarden.com/install | sh` を案内しているが、中身を確かめずに実行することになる。ここでは GitHub のリリースから落として、チェックサムを確かめる。

```sh
mkdir -p ~/Downloads/bws && cd ~/Downloads/bws
gh release download bws-v2.1.0 -R bitwarden/sdk-sm \
  -p 'bws-macos-universal-2.1.0.zip' -p 'bws-sha256-checksums-2.1.0.txt'
grep macos-universal bws-sha256-checksums-2.1.0.txt | shasum -a 256 -c
unzip bws-macos-universal-2.1.0.zip
mv bws ~/.local/bin/
bws --version
```

- 2026-11-10 時点の最新は bws v2.1.0（2026-05-21 公開）
- `shasum -c` が `OK` を出さなければ、そこで止める

## 3. 合言葉をキーチェーンに入れる

```sh
security add-generic-password -a "$USER" -s monban-trial-bws -w
```

`-w` の後ろに値を書かないと、入力を求められる。こうすると合言葉がシェルの履歴に残らない。

## 4. 人の手で通す

```sh
BWS_ACCESS_TOKEN="$(security find-generic-password -a "$USER" -s monban-trial-bws -w)" bws secret list
```

`MONBAN_TEST` が出れば成功。プロジェクトの ID（`projectId`）を控えておく。

## 5. エージェントから使う

2つの渡し方を、Claude Code と Codex の両方で試す。

**A. 人が値を入れてからエージェントを起動する**

```sh
BWS_ACCESS_TOKEN="$(security find-generic-password -a "$USER" -s monban-trial-bws -w)" \
  bws run --project-id <プロジェクトID> -- claude
```

（Codex は最後の `claude` を `codex` に変える）

起動したら、エージェントに「環境変数 `MONBAN_TEST` が入っているか、値は出さずに確かめて」と頼む。

**B. エージェントに `bws` を呼ばせる**

エージェントに、上の4の形のコマンドを実行させる。キーチェーンを読む段で、Mac が確認を出すかどうかを見る。

## 6. 見ること（ここが本題）

結果は、このファイルの下の「結果」に書く。

| 見ること | Claude Code | Codex |
|---|---|---|
| A で、エージェントは値そのものを読めたか（`echo` で出せるか） | | |
| A で、値が会話（モデルの文脈）に残ったか | | |
| B で、キーチェーンを読むときに Mac の確認が出たか | | |
| B で、許可リストや承認なしに全件取れてしまうか | | |
| 1回の呼び出しにかかった時間 | | |

予想（まだ確かめていない）：A は「モデルの文脈に残らない」だけで、エージェントが `echo` すれば値は読める（設計メモの「見せない」の段 B）。B は、確認が出なければ門番がない状態と同じ。

## 7. 片づける

- 合言葉を失効させる：`mac-trial` の「Access tokens」タブ → 合言葉のメニュー →「Revoke access token」
  - 失効しても、すでにログイン済みのマシンは**最大1時間**読み続けられる
  - `bws` は手元に `~/.config/bws/state` を作り、ログインの状態を保存する。消すと再ログインになる
- キーチェーンから消す：`security delete-generic-password -a "$USER" -s monban-trial-bws`

## 結果

（試したら書く）

## 出典

- [Secrets Manager CLI](https://bitwarden.com/help/secrets-manager-cli/)：入れ方、`BWS_ACCESS_TOKEN`、`bws secret list`、`bws run` の `--project-id`・`--no-inherit-env`、状態ファイル
- [マシンアカウント](https://bitwarden.com/help/machine-accounts/)：作り方、Can read と Can read, write の違い
- [アクセストークン](https://bitwarden.com/help/access-tokens/)：作り方、一度しか表示されない、失効しても最大1時間使える
- [プラン](https://bitwarden.com/help/secrets-manager-plans/)：無料版の上限
- [bitwarden/sdk-sm のリリース](https://github.com/bitwarden/sdk-sm/releases/tag/bws-v2.1.0)：bws v2.1.0、チェックサムのファイル

## 未確認

- 合言葉の期限に選べる長さ（ヘルプに一覧がない。画面で見る）
- `security add-generic-password` で入れた項目を `security` で読むとき、Mac が確認を出すか（出さない可能性が高い。手順 5B で確かめる）
- `bws run --no-inherit-env` を使っても `PATH` は引き継がれ、隔離にはならない（公式の注意書き）。エージェントの起動に使えるかは試していない
