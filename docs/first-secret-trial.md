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
| A で、エージェントは値そのものを読めたか（`echo` で出せるか） | はい | はい |
| A で、値が会話（モデルの文脈）に残ったか | はい（`echo` させると会話に出る） | はい |
| A で、合言葉（`BWS_ACCESS_TOKEN`）が子のプロセスに渡ったか | いいえ | いいえ |
| A で、親の `bws` の環境変数から合言葉を覗けたか（`ps eww`） | はい | サンドボックス内は不可。人が外での実行を許可すると可 |
| B で、キーチェーンを読むときに Mac の確認が出たか | いいえ（確認を足した後は出た） | サンドボックス内は項目が見えない。外での実行を許可すると確認が出た |
| B で、許可リストや承認なしに全件取れてしまうか | はい（合言葉が取れるので、`bws secret list` で全件取れる） | いいえ（Codex の承認とキーチェーンの確認の2段がある） |
| 1回の呼び出しにかかった時間 | 測っていない | 測っていない |

予想（まだ確かめていない）：A は「モデルの文脈に残らない」だけで、エージェントが `echo` すれば値は読める（設計メモの「見せない」の段 B）。B は、確認が出なければ門番がない状態と同じ。

## 7. 片づける

- 合言葉を失効させる：`mac-trial` の「Access tokens」タブ → 合言葉のメニュー →「Revoke access token」
  - 失効しても、すでにログイン済みのマシンは**最大1時間**読み続けられる
  - `bws` は手元に `~/.config/bws/state` を作り、ログインの状態を保存する。消すと再ログインになる
- キーチェーンから消す：`security delete-generic-password -a "$USER" -s monban-trial-bws`

## 結果

### 準備（2026-11-10）

- アカウントは「今すぐ登録」（Secrets Manager の無料版）から作った。サーバーは bitwarden.com。登録の流れで組織が自動で作られた
- 2段階認証は Password Manager の「設定」→「セキュリティ」→「2段階認証」で認証アプリを入れた。無料版でも FIDO2 WebAuthn の欄に「アップグレード」の印はなかった
- 合言葉の期限は、選択肢に「7日」があった
- `bws` v2.1.0 は照合 `OK`。署名の TeamIdentifier は `LTZ2PFU5D6`
- 手順4は成功。`bws secret list` は名前だけでなく**値まで全部**出す

### Claude Code（2026-11-10）

- A：`bws run` は秘密の値だけを子に渡し、合言葉は渡さない。ただし合言葉は親の `bws` プロセスの環境変数に残っていて、同じユーザーのエージェントが `ps eww` で覗けた
- A：エージェントは頼めば `echo` で値を出し、値は会話に入った。予想どおり「頼まなければ出てこない」だけ
- B：エージェントは `security find-generic-password` で、Mac の確認なしに合言葉を取り出せた（94文字）
- まとめ：同じユーザーで動く限り、合言葉はどの置き方でもエージェントから取れる。`bws` だけでは門番の代わりにならない。設計メモの「簡易モードは事故防止、厳格モードは別ユーザーかVM」を裏付ける

### キーチェーンの確認を足す（2026-11-10）

- `security add-generic-password` で作った項目は、「アクセスを許可する前に確認」になっていても、「常に許可」の一覧に `security` 自身が入っている。だから確認なしで読めていた
- 「キーチェーンアクセス」→ 項目 →「アクセス制御」で、一覧から `security` を消した
- そのあと手順 B を頼むと、Mac の確認が出た。拒否するとエージェントは取れず、Mac のパスワードを入れて許可すると取れた
- 効くこと：キーチェーンから合言葉を出す道に、人の承認が付く。門番を書く前の最低限の承認になる
- 効かないこと：
  - `bws run` の実行中は、親の `bws` の環境変数から `ps eww` で覗ける
  - 確認で「常に許可」を押すと `security` が一覧に戻り、元の状態に戻る
  - 許可するのは「`security` がこの項目を読むこと」だけで、誰が何のために読むかは画面に出ない（設計メモの「門番が作った事実を承認画面に出す」には届かない）
- 手順3に足すなら：作るときに `-T ""` を付ければ最初から一覧が空になるはず（まだ試していない）

### Codex（2026-11-10、codex-cli 0.142.3 から更新後、ChatGPT の無料プラン）

キーチェーンの確認を足した後に試した。

- A の1〜3は Claude Code と同じ。値は読めて会話に入る。合言葉は子に渡らない。止められず、許可も求められなかった
- `ps eww`：サンドボックスの中では `Operation not permitted` で止められた。Codex のコマンドがエラーを捨てていたので、最初は「ありません」と誤って答えた。Codex が外での実行を求め、人が1回だけ許可すると、親の `bws` の環境変数（30個）が見えた
- キーチェーン：サンドボックスの中では「項目が見つからない」（終了コード 44）。権限のエラーに見えないので、Codex は外での実行を求めなかった。外での実行を頼んで許可すると、Mac の確認が出た。拒否すると取れなかった（終了コード 128）
- まとめ：Codex では「サンドボックス → Codex の承認 → Mac の確認」の段がある。ただ、Codex の承認画面に出るのは長いコマンドと Codex が書いた理由で、合言葉が漏れうるかを人が見抜くのは難しい。中身を読まずに許可すれば Claude Code と同じになる

### 判断の材料

- `bws` とキーチェーンだけでは、同じユーザーのエージェントから合言葉を守れない（Claude Code は素通り、Codex は承認を押せば素通り）
- キーチェーンの確認は、足せば効く。手間はかからない。ただ、画面に出るのは「`security` が読む」だけ
- `bws run` は合言葉を子に渡さないが、親の環境変数に残る。`ps` を止める仕組み（Codex のサンドボックス、または別ユーザー）がないと覗ける
- 門番を書くなら、最低限ほしいのは：合言葉を門番だけが持ち、エージェントのプロセスから見えない場所に置くこと（別ユーザーか、`ps` で覗けない形）。承認画面に「どのエージェントが・どの秘密を・何のために」を出すこと

## 出典

- [Secrets Manager CLI](https://bitwarden.com/help/secrets-manager-cli/)：入れ方、`BWS_ACCESS_TOKEN`、`bws secret list`、`bws run` の `--project-id`・`--no-inherit-env`、状態ファイル
- [マシンアカウント](https://bitwarden.com/help/machine-accounts/)：作り方、Can read と Can read, write の違い
- [アクセストークン](https://bitwarden.com/help/access-tokens/)：作り方、一度しか表示されない、失効しても最大1時間使える
- [プラン](https://bitwarden.com/help/secrets-manager-plans/)：無料版の上限
- [bitwarden/sdk-sm のリリース](https://github.com/bitwarden/sdk-sm/releases/tag/bws-v2.1.0)：bws v2.1.0、チェックサムのファイル

## 未確認

- 合言葉の期限に選べる長さの全体（「7日」があることだけ確かめた）
- `security add-generic-password -T ""` で作れば、最初から確認が出るか
- `bws run --no-inherit-env` を使っても `PATH` は引き継がれ、隔離にはならない（公式の注意書き）。エージェントの起動に使えるかは試していない
