# ログの置き場を /var/db に移し、ファイルごと消えたら起動しないようにする

作成日: 2026-11-17

## なぜ

2026-11-14 の macOS の大型アップデート（26 → 27.2.1）で `/var/log` が作り直され、`/var/log/monban` が置き場ごと消えた。分かったことは2つ。

- `/var/log` の下は、OS のアップデートで残る保証がない。`/var/db` の下（`/var/db/monban`、`/var/db/monban-run`）は残っていた
- ログがファイルごと消えると、門番は空のファイルを作って seq 1 から書き始める。連鎖を確かめても何も見つからない。前のログが消えたと分かったのは、Pushover に残っていた seq 47 との照合だけだった

## 何をするか

- ログ（`audit.log`・`daemon.log`）の置き場を `/var/log/monban` から `/var/db/monban-log` に移す。持ち主とモードは今と同じ（ディレクトリは `_monban:_monbanusers` の 0750）
- 門番は、状態のディレクトリ（`/var/db/monban`）にログの最後の seq とハッシュの控え（`log-head`）を置く。置き換えるのは、起動の行を書いた後と、停止の行を書いた後
- 起動のとき、控えがあれば、ログにその seq の行があってハッシュが同じかを確かめる。ログがない・短い・ハッシュが違うなら起動しない。そのときは Pushover で「monban 起動しなかった」を1回送り、正常に終わる（launchd に起き直させない）。見張りも 15 分後から「止まっている」を送る
- 控えがないとき（初めて入れたとき・この版に入れ替えた最初の起動）は、確かめずに起動して控えを置く

守れるのは事故（置き場ごと消えた）まで。`_monban` か root になれる人は、ログと控えを両方書き換えられる。

わざとログを新しくするときは、控えも消す（`sudo rm /var/db/monban/log-head`）。

## 移し方

新しく入れるときは `scripts/install.sh` が `/var/db/monban-log` に置く。今動いている門番を移すときは、次を人のターミナルで（エージェントのいないウィンドウで）打つ。

1. 組み立てる（sudo は要らない）：

```sh
cd ~/monban-kit
git log --oneline -1
go test -race ./...
go build -o /tmp/monban-new ./cmd/monban
shasum -a 256 /tmp/monban-new
```

2. 止めて、実行ファイルを入れ替え、ログを移し、plist の置き場を書き換えて、起動する：

```sh
sudo launchctl bootout system/io.github.mediiiiium.monban
while sudo launchctl print system/io.github.mediiiiium.monban >/dev/null 2>&1; do sleep 1; done; echo "止まった"
sudo /usr/bin/install -o root -g wheel -m 0755 /tmp/monban-new /usr/local/libexec/monban/monban
shasum -a 256 /usr/local/libexec/monban/monban
sudo /usr/bin/install -d -o _monban -g _monbanusers -m 0750 /var/db/monban-log
sudo mv /var/log/monban/audit.log /var/log/monban/daemon.log /var/db/monban-log/
sudo rmdir /var/log/monban
sudo sed -i '' 's#/var/log/monban/#/var/db/monban-log/#g' /Library/LaunchDaemons/io.github.mediiiiium.monban.plist
plutil -lint /Library/LaunchDaemons/io.github.mediiiiium.monban.plist
grep -c monban-log /Library/LaunchDaemons/io.github.mediiiiium.monban.plist
sudo launchctl bootstrap system /Library/LaunchDaemons/io.github.mediiiiium.monban.plist
sleep 3; sudo launchctl print system/io.github.mediiiiium.monban | grep -E "^\tstate|pid"
rm /tmp/monban-new
```

見ること：

- 2つの `shasum` が同じ
- `grep -c` が `2`（`-log` の引数と、stderr の置き場）
- `state = running` で、pid が出る
- Pushover の「monban 起動」の seq が、前回の停止の知らせの seq の続き（連鎖がそのまま続いている）

3. 確かめる：

```sh
sudo ls -la /var/db/monban-log /var/db/monban
monban verify-log /var/db/monban-log/audit.log
```

- `audit.log` が `_monban:_monbanusers` の 0640、`daemon.log` が `_monban` の 0600
- `/var/db/monban/log-head` が `_monban` の 0600 である
- `verify-log` の最後の seq が、Pushover の「monban 起動」の seq と同じ

起動しないときは `sudo tail /var/db/monban-log/daemon.log` の最後の行に理由がある。

## 試す（任意）

ログを一時的によけて、起動しないことを確かめる：

```sh
sudo launchctl bootout system/io.github.mediiiiium.monban
while sudo launchctl print system/io.github.mediiiiium.monban >/dev/null 2>&1; do sleep 1; done
sudo mv /var/db/monban-log/audit.log /var/db/monban-log/audit.log.away
sudo launchctl bootstrap system /Library/LaunchDaemons/io.github.mediiiiium.monban.plist
sleep 3; sudo tail -3 /var/db/monban-log/daemon.log
```

「monban 起動しなかった」が届き、`daemon.log` に「ログがない（控えでは seq N まであった）」と出る。`audit.log` は作られていない。戻す：

```sh
sudo launchctl bootout system/io.github.mediiiiium.monban
while sudo launchctl print system/io.github.mediiiiium.monban >/dev/null 2>&1; do sleep 1; done
sudo mv /var/db/monban-log/audit.log.away /var/db/monban-log/audit.log
sudo launchctl bootstrap system /Library/LaunchDaemons/io.github.mediiiiium.monban.plist
```
