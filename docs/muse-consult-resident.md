# Muse への相談：門番 v1 の9段目（常駐化）

作成日: 2026-11-11
読んでほしいもの：[v1-plan.md](v1-plan.md)（「決めたこと」、2段目・3段目・7段目・8段目、9段目と10段目）、`cmd/monban/daemon.go`・`bws.go`・`checks.go`
前回の相談と回答：[muse-consult-kill-switch.md](muse-consult-kill-switch.md)、[muse-consult-kill-switch-response.md](muse-consult-kill-switch-response.md)

## 1. ここまでの進み

- 8段目（キルスイッチ）まで作り、人のターミナルで確かめた。どの段も、門番は自分の uid で、置き場は `/tmp` の下で動かしてきた
- 9段目で、門番を本番の形にする：別ユーザー `_monban`、launchd のデーモン、置き場とモード、合言葉と Pushover のファイルの置き方、入れ方と消し方
- 10段目で、その形のまま実験1（`ps eww` で合言葉が見えないか）をやり直す

## 2. 案（こう作るつもり）

### 2-1. ユーザーとグループ

- `_monban`：システムのユーザー（uid は 500 未満の空いている番号を入れるときに探す）。隠す、シェルは `/usr/bin/false`、ホームは `/var/empty`、パスワードなし
- `_monbanusers`：ソケットに繋いでよい人の専用グループ。入れるのは本人（uid 501）だけ。同じ uid のエージェントも、このグループに入っていることになる（uid が同じなので分けられない）
- 作るのは `dscl`・`dseditgroup`

### 2-2. 置き場

| 何 | 置き場 | 持ち主:グループ | モード |
|---|---|---|---|
| `monban` のバイナリ | `/usr/local/libexec/monban/monban`（`/usr/local/bin/monban` から指す） | root:wheel | 0755 |
| `bws` のバイナリ | `/usr/local/libexec/monban/bws`（GitHub のリリースから落とし、チェックサムと署名を確かめて写す） | root:wheel | 0755 |
| 許可リスト | `/usr/local/etc/monban/allowlist.json` | root:wheel | 0644 |
| 状態のディレクトリ | `/var/db/monban/` | _monban:_monban | 0700 |
| 合言葉 | `/var/db/monban/secret/token`（`bws-home/` も同じディレクトリの下） | _monban | 0700 / 0600 |
| Pushover | `/var/db/monban/secret/pushover.json` | _monban | 0600 |
| 止めた印 | `/var/db/monban/stopped` | _monban | 0600 |
| ログ | `/var/log/monban/audit.log`（ディレクトリは 0750 _monban:_monbanusers） | _monban:_monbanusers | 0640 |
| ソケット | 下の 3-1 | _monban:_monbanusers | ディレクトリ 0750、ソケット 0660 |
| 門番の画面のログ（stderr） | `/var/log/monban/daemon.log` | _monban | 0600 |

- `bws` を `~/.local/bin` のまま使わないのは、門番が「root か自分（`_monban`）の持ち物で、上のディレクトリも書き込めない」場所しか受け付けないため（ユーザーの持ち物だと、同じ uid のエージェントが差し替えられる）。今の確かめのまま、本番では自然に断られる
- ログを 0640 にするのは、7段目の Muse の答え（本人が `sudo` なしで読めるように）。同じグループのエージェントも読める件は、7段目の「防げないこと」に書いてある

### 2-3. launchd

- `/Library/LaunchDaemons/<ラベル>.plist`（root:wheel 0644）。`UserName` は `_monban`、`GroupName` は `_monbanusers`、`RunAtLoad`、`KeepAlive` は `{SuccessfulExit: false}`（落ちたら起き直し、止めたら止まる）
- 引数は全部 plist に書く（`-owner-uid 501` など）。環境変数には何も入れない（合言葉はファイルから読む）
- `StandardErrorPath` を `/var/log/monban/daemon.log` に

### 2-4. 止めた印（8段目の答えのとおり）

- `monban stop` で止めたときだけ、停止の行を書いた後に `/var/db/monban/stopped` を置く。中身は止めた時刻・uid・pid・ログの seq
- 起動のとき、印があれば起動しない。起動の失敗に理由と解除の方法（`sudo rm /var/db/monban/stopped`）を出す。印を置いた／見つけたことはログにも書く（起動しないので、書けるのは「印があるので起動しない」の1行。書くかどうかは 3-5）

### 2-5. 入れ方と消し方

- リポジトリに `scripts/install.sh` と `scripts/uninstall.sh` を置く。人が中身を読んでから `sudo` で動かす。エージェントは `sudo` を打たない
- 合言葉は、人が新しいウィンドウで `read -rs` で受け、`printf` で `_monban` の持ち物のファイルに書く（引数にも履歴にも出さない）。スクリプトは合言葉を扱わない
- 消し方：launchd から外す、plist・バイナリ・許可リストを消す、`/var/db/monban` を消す（合言葉も消える）、ユーザーとグループを消す。ログは残すか聞く。合言葉の失効は、8段目の手順と同じ（スクリプトからはできない）

### 2-6. コードの直し

- `daemon` に `-state-dir` を足す（止めた印の置き場）
- 本番の形（門番の euid が `-owner-uid` と違う）で `-pushover` がないなら起動しない。承認の番号が出るのは画面のログだけで、それは `_monban` しか読めないので、承認できなくなる（3-4）
- `defaultSocket` を 3-1 で決めた場所に直す

### 2-7. これで防げないこと（書くつもりの案）

- 本人の uid のエージェントは、`_monbanusers` に入っているので、ソケットに繋げて、ログ（0640）を読める。守りは、合言葉・Pushover のファイル・画面のログ（承認の番号）が読めないことと、許可リストを書き換えられないこと
- `sudo` を取れれば、全部守れない（モデルの外）。本人が `sudo` を打つ場面（入れる・消す・止めた印を消す）で、エージェントが同じターミナルにいないこと
- 許可リストは root の持ち物なので、直すたびに `sudo` が要る。面倒で `approval: none` を増やしたくなる

## 3. 聞きたいこと

### 3-1. ソケットの置き場

- `/var/run` は root:daemon の 0775 で、`_monban` はその下にディレクトリを作れない。起動のたびに消えるかもしれない（確かめていない。今の Mac では、起動の時刻より古いファイルが `/var/run` に残っているようにも見えた）
- 案：
  - A：消えない場所に置く。例：`/var/db/monban-run/monban.sock`（入れるときに 0750 _monban:_monbanusers で作る）。`/var/db/monban` の中は 0700 なので、グループが通れず使えない
  - B：`/var/run/monban` を入れるときに作り、消えていたら、起動の前に root で作り直す（launchd の別の仕事か、`/etc/` のしかけ）。重い
  - C：launchd の `Sockets` でソケットを作らせる。Go から `launch_activate_socket` を呼ぶのに cgo が要り、持ち主とモードを自分で確かめる今の形とも合わない
- A でよいか。ほかに標準的な置き場があるか

### 3-2. 画面のログ（stderr）の置き場と、承認の番号

- 本番でも、番号は画面のログに出したほうがよいか（Pushover が届かなかったときに、`sudo` で読んで承認する道）。それとも本番では画面のログから番号を消すか
- `StandardErrorPath` のファイルを launchd が誰の持ち物で作るか、確かめていない。入れるときに 0600 _monban で先に作っておくつもり

### 3-3. ログの置き場

- `/var/log/monban/` でよいか。`newsyslog` などの回しはしない（7段目で「回さない」と決めた）。大きくなる速さは1要求で1〜3行

### 3-4. Pushover を本番で必須にするか

- 2-6 の案。試すとき（euid が `-owner-uid` と同じ）は今のまま、なくても動く

### 3-5. 止めた印

- 印があるときに「起動しない」の1行をログに書くか。書くと、launchd の `KeepAlive` が起き直させる形ではないので（止めたときは正常に終わっている）、再起動のたびに1行ずつ増えるだけ。書けば「止めたまま、何回起動しようとしたか」が残る
- 印の中身を門番が信じるか（偽の印で困るのは「起動しない」だけなので、中身は人が読むためのもので、門番は「あるかないか」だけを見るつもり）

### 3-6. 入れ方

- スクリプト1本にするか、人が1行ずつ打つ手順書にするか。今までの試しは、手順を1つずつ貼ってもらう形だった
- `_monban` の uid の選び方。ほかのソフトと重ならない番号を、入れるときに探す（空いている 400 番台など）でよいか

### 3-7. 2-7 の書き方

- 言い過ぎも言い足りなさもないか

## 決まり（相談でも守る）

- 合言葉や本物のパスワードは、このリポジトリにも会話にも出さない
- 「見せない」の効果を盛らない
