# 門番 v1 の10段目：本番の形でもう一度確かめる

作成日: 2026-11-11
前の実験：[pre-v1-experiments.md](pre-v1-experiments.md) の実験1（`nobody` の `sleep` の複製で、別ユーザーの環境変数は `ps eww` で見えなかった）
相談と回答：[muse-consult-production-check.md](muse-consult-production-check.md)、[muse-consult-production-check-response.md](muse-consult-production-check-response.md)

## 何を確かめるか

実験1は `nobody` と `sleep` の複製で行った。本番の形は違うところが2つある。

- 門番は自作の Go のバイナリ（`go build` の署名）で、launchd のデーモンとして `_monban` で動く
- 合言葉が環境変数に入るのは、門番が取り出しのたびに起こす `bws` の子だけ（`BWS_ACCESS_TOKEN`。1回あたり1秒ほどで消える）。門番そのものは合言葉をメモリに持つだけで、環境変数には入れない作り

だから2つに分けて見る。

- **A. 自作の Go のバイナリ**：OS のプログラムと同じく環境変数が隠れる性質なのか、を対照つきで見る。隠れる性質なら、本番で「見えない」が出ても、別ユーザーだからとは言えない
- **B. 本番の門番と本物の合言葉**：取り出しの最中の `bws` の子を捕まえて、本人の uid から `BWS_ACCESS_TOKEN` が見えないこと、root からは見えること（入っていたこと）を見る。門番そのものの環境変数に合言葉がないことも root で見る。`ps` とは別の層として、合言葉のファイルが本人の uid から読めないことも見る
- A と B の受け持ち：A は「相手のプロセスの uid・読む側の uid・バイナリの署名」の問い（launchd を通るかは関係しない）。B-2 は launchd が渡す環境変数の中身の問い

## 決まり

- 合言葉（アクセストークン）は、このリポジトリにも AI との会話にも貼らない。B は**エージェントのいない新しいウィンドウ**で人が行う
- 画面に出すのは、行が出たか（`1`/`0`）と、`BWS_ACCESS_TOKEN=` を含む行の数だけ。`ps eww` の出力そのものは出さない・貼らない（値が混ざる）
- 取り出す秘密は `MONBAN_TEST`（値はダミー）。合言葉は、`MONBAN_TEST` だけに届くマシンアカウントのもの
- 承認の番号が出ている画面は、まだ有効なうちは貼らない
- root からは見える。エージェントが `sudo` を取れれば守れない（モデルの外）。`sudo` を打つウィンドウにエージェントを置かない。終わったら `sudo -k`

## A. 自作の Go のバイナリ（合言葉は使わない）

目印は秘密ではない、ただの乱数。実験1と同じ形で、`sleep` の代わりに Go で書いた sleep を使い、`nobody` の代わりに `_monban` で動かす。

```sh
F=$(mktemp /tmp/monban-canary.XXXXXX)
echo "CANARY_MONBAN_$(openssl rand -hex 4)" > "$F"
chmod 644 "$F"
CANARY=$(cat "$F")

D=$(mktemp -d /tmp/monban-gosleep.XXXXXX)
chmod 755 "$D"
printf 'package main\n\nimport "time"\n\nfunc main() { time.Sleep(300 * time.Second) }\n' > "$D/main.go"
(cd "$D" && go mod init gosleep >/dev/null 2>&1 && go build -o gosleep .)
S=$D/gosleep
chmod 755 "$S"
codesign -dv "$S" 2>&1 | grep -i signature
```

- 最後の行は、署名の種類を見るだけ（`adhoc` と出るはず。`go build` が付ける署名）

```sh
sh -c 'MONBAN_CANARY=$(cat "$1") exec "$2"' sh "$F" "$S" &
SELF_PID=$!
sleep 1
ps eww -p "$SELF_PID" | grep -q "$CANARY" && echo "対照：見えた" || echo "対照：見えない"
```

```sh
sudo -v
```

（パスワードを入れてから、残りを貼る）

```sh
(cd / && sudo -u _monban sh -c 'MONBAN_CANARY=$(cat "$1") exec "$2"' sh "$F" "$S") &
sleep 1
OTHER_PID=$(pgrep -u _monban -n -f "$S")
echo "OTHER_PID=$OTHER_PID"
ps -o user,pid,comm -p "$OTHER_PID"
ps eww -p "$OTHER_PID" | grep -q "$CANARY" && echo "_monban：見えた" || echo "_monban：見えない"
sudo ps eww -p "$OTHER_PID" | grep -q "$CANARY" && echo "root：見えた" || echo "root：見えない"
```

片づける：

```sh
kill "$SELF_PID" 2>/dev/null
sudo kill "$OTHER_PID"
rm -rf "$F" "$D"
```

見ること：対照で「見えた」、`ps -o` に `_monban` の行が出る、`_monban` で「見えない」、root で「見えた」。対照で見えなければ、そこで止める。

## B. 本番の門番と本物の合言葉

いま入っている門番は、ダミーの合言葉と、仮の ID の許可リストで動いている。本物に入れ替えてから見る。

### B-1. 入れ替える（エージェントのいない新しいウィンドウ）

1. Bitwarden のウェブの画面で、`MONBAN_TEST` の秘密の ID を控える（ID は秘密ではない）
2. 許可リストの `id` を、控えた ID に直す（root の持ち物なので `sudo`）：

   ```sh
   sudo -v
   ```

   （パスワードを入れてから、残りを1つずつ打つ。`read` の行と後ろの行を一度に貼ると、後ろの行が ID として読まれる）

   ```sh
   read 'ID?MONBAN_TEST の ID: '
   echo "$ID" | grep -Eq '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' && echo "形：OK" || echo "形：違う"
   sudo sed -i '' "s/0a1b2c3d-0000-0000-0000-000000000000/$ID/" /usr/local/etc/monban/allowlist.json
   cat /usr/local/etc/monban/allowlist.json; echo
   ls -l /usr/local/etc/monban/allowlist.json
   ```

   見るところ：`id` だけが変わり、`MONBAN_TEST`・`/usr/bin/printenv`・承認ありのまま。root wheel の `-rw-r--r--` のまま

3. 合言葉を入れる（今のダミーを上書きする）。合言葉は、Secrets Manager のマシンアカウントの「Access tokens」で作ったときに1回だけ出る、`0.` で始まる長い文字列（UUID の形の ID ではない）。先に `sudo -v` を通しておく（パスワードを聞かれると、一緒に貼った `unset T` がパスワードとして読まれる）：

   ```sh
   read -rs 'T?合言葉: '; echo
   (cd / && printf '%s\n' "$T" | sudo -u _monban /bin/sh -c 'umask 077; cat > /var/db/monban/secret/token')
   unset T
   ```

4. 門番を起動し直す（合言葉と許可リストは起動のときに読む）：

   ```sh
   sudo launchctl kickstart -k system/io.github.mediiiiium.monban
   sudo ls -la /var/db/monban/secret
   sudo grep -c '^0\.' /var/db/monban/secret/token
   ```

   `token` が `_monban` の 0600（`-rw-------`）のままか、大きさが 90 バイトほどで、最後が `1`（`0.` で始まる）か見る

### B-2. 合言葉のファイルと、門番そのもの

本人の uid から、合言葉のファイルが読めないか（`ps` とは別の層。ファイルの権限）：

```sh
cat /var/db/monban/secret/token >/dev/null && echo "読めた" || echo "読めない"
```

- 「Permission denied」と「読めない」が出ればよい。中身は捨てる形なので、読めても画面には出ない。「読めた」なら、そこで止めて合言葉を失効させる

（任意）引数に秘密が入っていないことを、目で1回見る：

```sh
launchctl print system/io.github.mediiiiium.monban | grep -A20 'arguments'
```

- 出るのはパス（`-token-file` など）だけのはず。合言葉そのものが出ていないか

門番そのものの環境変数：

```sh
M=$(pgrep -u _monban -x monban)
echo "M=$M"
ps -o user,pid,comm -p "$M"
ps eww -p "$M" | grep -c 'BWS_ACCESS_TOKEN='
sudo ps eww -p "$M" | grep -c '_SERVICE_NAME='
sudo ps eww -p "$M" | grep -c 'BWS_ACCESS_TOKEN='
```

- 1行目の数：本人の uid から、門番の環境変数に `BWS_ACCESS_TOKEN=` があるか（`0` のはず）
- 2行目の数：root からは、launchd が渡す環境変数（`XPC_SERVICE_NAME` など）が見えるか（`1` のはず。見えれば、root からは Go のバイナリの環境変数が読めるという確かめになる）
- 3行目の数：root から見ても、門番の環境変数に合言葉がないか（`0` のはず。合言葉はメモリだけ）
- 2行目が `0` なら、3行目の `0` は何も言えない

### B-3. 取り出しの最中の `bws` の子

`bws` の子は1秒ほどしか生きないので、見張りを裏で回しておき、その間に取り出す。見張りは、最初に見つけた `bws` の子について、数だけを出して終わる（最長3分）。

- 捕まえた回を1回で終わらせず、**下の見張りと取り出しを3回**繰り返し、3回とも同じ数が出ることを見る（取り逃した回は数えない）
- root の `ps eww` の出力は、行の有無と数を同じ瞬間から取るためにシェルの変数に一瞬持つ。画面・ファイル・履歴には出さず、数だけ出して `unset` する

ウィンドウ1（エージェントのいない新しいウィンドウ。`sudo -v` は単独で打ち、パスワードを入れてから残りを貼る）：

```sh
sudo -v
```

```sh
(
  end=$((SECONDS + 180))
  while [ $SECONDS -lt $end ]; do
    P=$(pgrep -u _monban -x bws | head -1)
    [ -n "$P" ] || continue
    U=$(ps eww -p "$P")
    R=$(sudo -n ps eww -p "$P")
    echo "見張り：bws の子 pid $P"
    echo "  本人の uid：行 $(printf '%s\n' "$U" | grep -c "^ *$P ")、合言葉 $(printf '%s\n' "$U" | grep -c 'BWS_ACCESS_TOKEN=')"
    echo "  root：行 $(printf '%s\n' "$R" | grep -c "^ *$P ")、合言葉 $(printf '%s\n' "$R" | grep -c 'BWS_ACCESS_TOKEN=')"
    unset U R
    exit 0
  done
  echo "見張り：3分の間に bws の子を捕まえられなかった"
) &
monban run MONBAN_TEST -- /usr/bin/printenv MONBAN_TEST
```

ウィンドウ2：Pushover に届いた番号で承認する（番号の出ている画面は貼らない）：

```sh
monban approve <番号>
```

見ること：

- 本人の uid：行 `1`、合言葉 `0`
- root：行 `1`、合言葉 `1`
- root の行が `0` なら、`bws` の子が先に消えた。そのときは何も言えないので、数えずにもう一度取り出す（ウィンドウ1の手順をもう一度）
- 捕まえた回が3回になるまで繰り返し、3回とも同じ数か
- `monban run` がダミーの値を出して終わること（本物の合言葉で `bws` が取り出せた確かめ）

### B-4. 片づける

```sh
sudo -k
```

- 門番は本物の合言葉のまま動かしておく（届くのは `MONBAN_TEST` だけ。本番の形を動かし続けるのも10段目の意味）。最初の本物の秘密を入れるときに、新しい合言葉に替える
- やめるなら `monban stop` で止め、出てくる手順で合言葉を失効させる
- 確かめた後に、`monban verify-log /var/log/monban/audit.log` で連鎖が合うか見る

## 結果

2026-11-11、macOS（Darwin 25.5.0）、Go 1.27.3、`bws` v2.1.0。人のターミナル（zsh）で、エージェントのいないウィンドウで行った。

### A. 自作の Go のバイナリ

| 見ること | 結果 |
|---|---|
| `go build` の署名 | `adhoc` |
| 対照（自分の uid）で目印が見えたか | **見えた** |
| `_monban` のプロセスの行は `ps` に出たか | 出た（`_monban`、`gosleep`） |
| 本人の uid から、`_monban` の目印が見えたか | **見えない** |
| root で目印が見えたか | 見えた |

自作の Go のバイナリは、Apple のプログラムと違って同じ uid からは環境変数が見える。別ユーザー（`_monban`）にすると見えない。B の「見えない」は、バイナリの性質ではなく別ユーザーだから、と言える。

### B. 本番の門番と本物の合言葉

- 入れ替え：許可リストの `id` を `MONBAN_TEST` の本物の ID にした。合言葉は、`mac-trial`（`monban-trial` のプロジェクトを読むだけ）に新しく作った `monban-daemon`（期限30日）。最初の試しの合言葉は、エージェントにキーチェーンから取られたことがあるので失効させた
- 1度目は合言葉の代わりに UUID（ID）を貼っていた。`token` が 37 バイトだったので気づいた。手順に、大きさと `0.` で始まるかの確かめを足した

| 見ること | 結果 |
|---|---|
| 本人の uid から、合言葉のファイルを読めたか | **読めない**（Permission denied） |
| 門番のプロセスの行 | 出た（`_monban`、`/usr/local/libexec/monban/monban`） |
| 本人の uid から、門番の `BWS_ACCESS_TOKEN=` | `0` |
| root から、門番の `_SERVICE_NAME=`（root の目が効いているか） | `1` |
| root から、門番の `BWS_ACCESS_TOKEN=` | `0`（合言葉は門番の環境変数にない） |

取り出しの最中の `bws` の子（捕まえた3回とも、取り逃しなし）：

| 回 | 本人の uid（行・合言葉） | root（行・合言葉） | 取り出し |
|---|---|---|---|
| 1 | 1・**0** | 1・1 | `hello-monban` |
| 2 | 1・**0** | 1・1 | `hello-monban` |
| 3 | 1・**0** | 1・1 | `hello-monban` |

- `verify-log`：連鎖は合っている（25 行）。最後の行が停止の行ではないのは、門番が動いているため
- `launchctl print` は見ていない（任意）
- 見張りが `exit` するときに、ターミナルが「Saving session...」を出す。害はない

まとめ：root でない本人の uid からは、本番の形（launchd のデーモン、`_monban`、`bws` の子）でも、合言葉は `ps eww` で見えず、ファイルも読めなかった。同じ瞬間に root からは見えたので、合言葉は確かに `bws` の子の環境変数に入っていた。門番そのものの環境変数には、root から見ても入っていなかった。

- README に書くときは、無条件の「見えない」ではなく、いつ・どの形で・root の対照つきで確かめた結果として書く

## これで防げないこと

- root からは見える。エージェントが `sudo` を取れれば、合言葉は守れない（モデルの外）
- 見るのは `ps` の道だけ。デバッガの接続など、ほかの道は root か管理者の認証が要る前提で、ここでは試さない
- `monban run` が受け取った値（`MONBAN_TEST` の値）は本人の uid の中にあり、注入先のコマンドの環境変数は、同じ uid から `ps eww` で見える（v1 の「これで防げないこと」のとおり）。ここで確かめるのは合言葉の側だけ
- 見張りが捕まえるのは1回の取り出しの1つの瞬間。ずっと見えないことの証明ではなく、macOS の `ps` の決まり（別ユーザーの環境変数は出さない）が、本番の形でも効いていることの確かめ
