# 最初の本物の秘密を入れる

作成日: 2026-11-12
前の段：[production-check.md](production-check.md)（10段目。B-4 で「最初の本物の秘密を入れるときに、新しい合言葉に替える」と決めた）

## 何をするか

いま門番が届くのは、ダミーの `MONBAN_TEST` だけ。ここで本物の秘密を1つ入れ、同時に合言葉を新しいものに替えて、古いものを失効させる。

- 入れる秘密：**Pushover のアプリのトークン**。手元用に新しく1本発行し、Secrets Manager では `PUSHOVER_TOKEN` という名前にする
- 使うコマンド：`/usr/local/libexec/monban-cmds/pushover-notify`（このリポジトリの [tools/pushover-notify](../tools/pushover-notify) を root の持ち物の場所に写したもの）
- `MONBAN_TEST` は残す（合言葉や門番の調子を、本物を使わずに確かめる道として使う）

### この秘密を選んだ理由

- 漏れたときの被害：通知を勝手に送られるだけ。お金・書き込み・個人の情報には届かない
- 失効：Pushover の画面でアプリを消せば、すぐ止まる
- 出力：`pushover-notify` はトークンを画面に出さない。伏せる処理（v1.5）がなくても、エージェントの会話に残らない
- **新しく発行する理由**：tony（GitHub Actions）が使っているトークンは GitHub Secrets にある。同じ値を Secrets Manager にも入れると、README で「最後の手段」とした二重持ちになる。手元用に別の1本を作れば、README の勧め1（エージェント用に別の資格情報を発行する）のとおりになり、CI のほうを止めずに失効もできる
- 最初の1本にしないもの：X のトークン（投稿できる）、`ANTHROPIC_API_KEY`（お金に直結する。使用量に上限を付けて2本目ならあり）、`TONY_TOKEN`（予定やアカウントの情報に届く）

### コマンドを ~/tony の notify.py にしない理由

- 許可リストは `python3` を書けない（何でも実行できるコマンドとして弾く）。書けるのはスクリプトのパスで、`notify.py` は実行の権限がなく、tony のリポジトリのファイルの権限を替えることになる
- `~/` の下は本人の uid の持ち物なので `userOwned` の印が要り、同じ uid のエージェントが中身を替えられる（トークンを出力するように書き換えられる）。root の持ち物の場所に、小さな専用のスクリプトを置くほうが筋がよい
- 先頭行は `#!/usr/bin/python3 -I`。`-I` で、`PYTHONPATH` など `PYTHON` で始まる環境変数と、ユーザーの site-packages を読まない

## 決まり

- トークンも合言葉も、このリポジトリにも AI との会話にも貼らない。値を入れるのは Bitwarden のウェブの画面と、**エージェントのいない新しいウィンドウ**だけ
- 秘密の ID（UUID の形）は秘密ではない。合言葉は `0.` で始まる長い文字列で、ID とは別のもの
- 承認の番号が出ている画面は、まだ有効なうちは貼らない
- `sudo -v` は単独で打ち、パスワードを入れてから残りを貼る。`read` の行は1行ずつ打つ（後ろの行を一緒に貼ると、それが入力として読まれる）
- 終わったら `sudo -k`
- マシンアカウントは Can read だけ（変えない決まり。新しく作るものも同じ）

## 1. Pushover で手元用のアプリを作る

1. Pushover のウェブの画面で、アプリ（Application）を1つ新しく作る。名前は門番の承認の通知と見分けがつくもの（例：`monban-local`）
2. 出てきた API トークンは、まだどこにも貼らない（次の手順で Bitwarden の画面に直接入れる）

承認の通知に使っている門番のアプリ（`pushover.json`）とは別のアプリにする。分けておくと、こちらを失効させても承認の通知は止まらない。

## 2. Secrets Manager に入れ、新しい合言葉を作る（Bitwarden のウェブの画面）

1. 組織 `monban` の Secrets Manager で、新しいプロジェクトを作る（例：`local-notify`）
2. そのプロジェクトに秘密を1つ作る。名前は `PUSHOVER_TOKEN`、値は手順1のトークン
3. 作った秘密の ID を控える（秘密ではない）
4. **マシンアカウントを新しく作る**（例：`monban-gatekeeper`）。「Projects」に `local-notify` と `MONBAN_TEST` のプロジェクトを、どちらも **Can read** で足す
5. 新しいマシンアカウントの「Access tokens」で、新しい合言葉を作る。1回だけ出るので、手順4で入れるまで画面を閉じない
6. 古いマシンアカウントには**何も足さない**。古い合言葉は、まだ失効させない（新しい合言葉で動くのを確かめてから。手順5）

今のマシンアカウントに `local-notify` を足さない理由：合言葉が届く範囲は、マシンアカウントのプロジェクトで決まる。今のマシンアカウントに足すと、これまでいくつかのウィンドウで扱ってきた古い合言葉も、失効させるまで本物の秘密に届いてしまう。新しいマシンアカウントにすれば、古い合言葉は最後まで `MONBAN_TEST` にしか届かない。

## 3. コマンドを root の持ち物の場所に入れる（エージェントのいない新しいウィンドウ）

入れる前に、写すスクリプトの中身を目で見る（同じ uid のエージェントは、作業ツリーのファイルを替えられる）：

```sh
cd ~/monban-kit
git status --short tools/
git diff HEAD -- tools/
cat tools/pushover-notify
```

- `git status` と `git diff` が何も出さないこと（コミットした中身のまま）。出たら、そこで止める
- 中身に、トークンを出力する行がないこと

```sh
sudo -v
```

```sh
sudo /usr/bin/install -d -o root -g wheel -m 0755 /usr/local/libexec/monban-cmds
sudo /usr/bin/install -o root -g wheel -m 0755 tools/pushover-notify /usr/local/libexec/monban-cmds/pushover-notify
cmp tools/pushover-notify /usr/local/libexec/monban-cmds/pushover-notify && echo "同じ中身"
ls -ld /usr/local/libexec/monban-cmds
ls -l /usr/local/libexec/monban-cmds/pushover-notify
```

- 両方とも root wheel で、ディレクトリが `drwxr-xr-x`、ファイルが `-rwxr-xr-x`
- 置き場を `/usr/local/libexec/monban` にしないのは、`scripts/uninstall.sh` がそこを丸ごと消すから

## 4. 許可リストに足し、合言葉を替えて起動し直す（同じウィンドウ）

許可リストを書き換える。`MONBAN_TEST` の項目は今のまま残し、`PUSHOVER_TOKEN` の項目を足す。承認は書かない（毎回の承認になる）：

```sh
read 'ID?PUSHOVER_TOKEN の ID: '
```

```sh
echo "$ID" | grep -Eq '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' && echo "形：OK" || echo "形：違う"
```

「形：OK」のときだけ続ける：

```sh
sudo cat /usr/local/etc/monban/allowlist.json; echo
printf '{"secrets":[{"name":"MONBAN_TEST","id":"5676dd90-9733-4fe5-b61e-b4fe00ae43f8","allow":[{"command":"/usr/bin/printenv","agents":["*"]}]},{"name":"PUSHOVER_TOKEN","id":"%s","allow":[{"command":"/usr/local/libexec/monban-cmds/pushover-notify","agents":["*"]}]}]}\n' "$ID" | sudo tee /usr/local/etc/monban/allowlist.json >/dev/null
cat /usr/local/etc/monban/allowlist.json; echo
ls -l /usr/local/etc/monban/allowlist.json
unset ID
```

- 1行目で出た `MONBAN_TEST` の `id` が `5676dd90-…` と同じか見る。違ったら、書き換える前に止めて、2行目の `MONBAN_TEST` の `id` を直す
- 書き換えた後、`MONBAN_TEST` の項目が前と同じで、`PUSHOVER_TOKEN` の項目が1つ増えている
- root wheel の `-rw-r--r--` のまま（`tee` は今のファイルに書くので、持ち主とモードは変わらない）
- `agents` を `["*"]` にするのは、名札が認証ではないから（守りは承認）。Claude Code からも使う前提

合言葉を新しいものに替える（今のものを上書きする）：

```sh
read -rs 'T?新しい合言葉: '; echo
```

```sh
(cd / && printf '%s\n' "$T" | sudo -u _monban /bin/sh -c 'umask 077; cat > /var/db/monban/secret/token')
unset T
```

起動し直す（合言葉と許可リストは、起動のときに読む）：

```sh
sudo launchctl kickstart -k system/io.github.mediiiiium.monban
sleep 2
sudo launchctl print system/io.github.mediiiiium.monban | grep -E '^\s*(state|pid|last exit code)'
sudo ls -la /var/db/monban/secret
sudo grep -c '^0\.' /var/db/monban/secret/token
```

- `state = running` で、`pid` が出ている
- `token` が `_monban` の 0600（`-rw-------`）、最後の数が `1`
- 動いていなければ、`sudo tail -20 /var/log/monban/daemon.log` で理由を見る（許可リストの書き間違いや、コマンドの置き場の確かめで止まっていることが多い）。古い承認の番号が出ていることがあるが、期限の切れたもので、貼らなければよい

## 5. 新しい合言葉で通し、古いものを失効させる

### 5-1. 新しい合言葉で `MONBAN_TEST` が取れるか

ウィンドウ1：

```sh
monban run MONBAN_TEST -- /usr/bin/printenv MONBAN_TEST
```

ウィンドウ2（Pushover に届いた番号で）：

```sh
monban approve <番号>
```

ダミーの値が出れば、新しい合言葉で `bws` が取り出せている。

### 5-2. 本物の秘密を通す

ウィンドウ1（ユーザーキーは画面にも履歴にも出さない）：

```sh
read -rs 'PU?Pushover のユーザーキー: '; echo
```

```sh
PUSHOVER_USER=$PU monban run PUSHOVER_TOKEN -- /usr/local/libexec/monban-cmds/pushover-notify "門番を通った1本目" "monban"
unset PU
```

ウィンドウ2：承認の通知（門番のアプリ）に出た番号で承認する。

```sh
monban approve <番号>
```

見ること：

- ウィンドウ1に「送った」と request の ID が出る。トークンは出ない
- スマホに通知が2本届く：先に承認の通知（門番のアプリ）、後に「門番を通った1本目」（手順1で作った `monban-local`）。アプリの名前が違うこと

### 5-3. 古い合言葉を失効させる

1. Bitwarden のウェブの画面で、**古いマシンアカウント**の「Access tokens」から、古い合言葉を失効させる。古いマシンアカウントがもう要らなければ、アカウントごと消してよい
2. 5-1 をもう一度やり、ダミーの値が出ること（門番が使っているのが新しいマシンアカウントの合言葉だった確かめ）
3. 失効させても、古い合言葉で最大1時間は読める（キルスイッチと同じ。[design.md](design.md) の「正直に書くこと」）。古い合言葉が届くのは `MONBAN_TEST` だけなので、この1時間の間も本物の秘密には届かない

## 6. 記録を確かめて片づける

```sh
monban verify-log /var/log/monban/audit.log
tail -6 /var/log/monban/audit.log
sudo -k
```

- 連鎖が合う
- 最後の数行に `PUSHOVER_TOKEN` の要求・承認・渡した記録がある。値は書かれていない（書くのは名前と、コマンドの絶対パス、引数の数とハッシュだけ）
- 終わったら、`README.md` の「いまの段階」と、この下の「結果」を書く

## Claude Code から使う

人のターミナルで通った後に、Claude Code から1回使う（[design.md](design.md) の「次の一手」の「1本だけ手で通す」）。

- Claude Code には `MONBAN_AGENT=claude monban run PUSHOVER_TOKEN -- /usr/local/libexec/monban-cmds/pushover-notify "<本文>"` を打たせる。`PUSHOVER_USER` は、Claude Code を起こすシェルで先に入れておく
- 承認は人がエージェントのいないウィンドウで打つ。承認の画面に出る名札（`claude`）・コマンド・引数を見てから承認する
- ログの名札が `claude` になっていること

## やめるとき

- このトークンだけ止める：Pushover の画面で `monban-local` のアプリを消す。Secrets Manager の `PUSHOVER_TOKEN` も消し、許可リストから項目を外して起動し直す
- 門番ごと止める：`monban stop`（出てくる手順で合言葉を失効させる）

## これで防げないこと

- 同じ uid のエージェントは、`monban run` を使わずにソケットへ直接書けば、許可されたコマンドを名乗って値を受け取れる（承認は要る）。コマンドを root の場所に置いたのは、うっかりの書き換えを防ぐためで、悪意への守りではない
- `/usr/bin/python3` は Xcode のコマンドラインツールへの入口で、`DEVELOPER_DIR` などの環境変数で行き先が変わりうる。`-I` が効くのは Python の中の話。上と同じく、守りの実体は承認と uid の分離
- `pushover-notify` が動いている1秒ほどの間、`PUSHOVER_TOKEN` はその環境変数にあり、同じ uid から `ps eww` で見える
- トークンを手にしたエージェントは、通知を送れる。承認の通知に似せた文を送ってきても、アプリの名前が違う（`monban-local`）。番号は門番が出したものしか通らないので、偽の通知の番号で承認しても何も起きない
- エージェントが `sudo` を取れれば、全部守れない（モデルの外）

## 結果

（まだ行っていない）
