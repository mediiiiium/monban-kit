# 見張り（monban watch）を、動いている本番の形に足す

作成日: 2026-11-17

## なぜ

2026-11-14 の macOS の大型アップデート（26 → 27.2.1）で `/var/log/monban` が消え、門番は 11/17 まで 3 日半、起動できずにいた。launchd が `daemon.log` を開けずに失敗していたので、門番のコードまで届かず、Pushover の知らせも飛ばなかった。止まっていると気づいたのは、別の作業で `launchctl print` を見たときだった。

門番は止まると秘密を渡さない側に倒れるので、漏れはない。困るのは「止まっているのに、誰も気づかない」こと。

## 何をするか

門番とは別の launchd の仕事（`io.github.mediiiiium.monban.watch`）を `_monban` で 15 分ごとに動かし、`monban watch` で外から見る。

- 門番が動いていない（launchd の state が `running` でない、またはソケットがない）なら、Pushover で「monban 止まっている」を送る
- 止めた印（`/var/db/monban/stopped`）があるときは、人が `monban stop` で止めたので送らない
- 送ったら `/var/db/monban/watch-alerted` を置き、6 時間は黙る。戻ったら「monban 戻った」を1回送って消す
- 見張りは `/var/log` に何も書かない。plist にも stderr の置き場を書かない（ログの置き場が消えても動く）

見張りが送れなかったときの失敗は、どこにも残らない（launchd の `last exit code` だけ）。見張りを見張るものはない。

## 入れ方

新しく入れるときは `scripts/install.sh` が見張りの plist も置く。いま動いている門番に足すときは、次を人のターミナルで（エージェントのいないウィンドウで）打つ。

1. 実行ファイルを、見張りの入った版に入れ替える（[v1.5-install.md](v1.5-install.md) の 2 と同じ手順）。

2. 見張りの plist を置いて、起動する（中身は `scripts/install.sh` の 7b と同じ。install.sh は「もう入っている」で止まるので、手で置く）：

```sh
cat > /tmp/monban-watch.plist <<'EOF'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>io.github.mediiiiium.monban.watch</string>
	<key>ProgramArguments</key>
	<array>
		<string>/usr/local/libexec/monban/monban</string>
		<string>watch</string>
		<string>-label</string>
		<string>io.github.mediiiiium.monban</string>
		<string>-socket</string>
		<string>/var/db/monban-run/monban.sock</string>
		<string>-state-dir</string>
		<string>/var/db/monban</string>
		<string>-pushover</string>
		<string>/var/db/monban/secret/pushover.json</string>
	</array>
	<key>UserName</key>
	<string>_monban</string>
	<key>GroupName</key>
	<string>_monbanusers</string>
	<key>Umask</key>
	<integer>63</integer>
	<key>RunAtLoad</key>
	<true/>
	<key>StartInterval</key>
	<integer>900</integer>
</dict>
</plist>
EOF
plutil -lint /tmp/monban-watch.plist
sudo /usr/bin/install -o root -g wheel -m 0644 /tmp/monban-watch.plist /Library/LaunchDaemons/io.github.mediiiiium.monban.watch.plist
rm /tmp/monban-watch.plist
sudo launchctl bootstrap system /Library/LaunchDaemons/io.github.mediiiiium.monban.watch.plist
sleep 3; sudo launchctl print system/io.github.mediiiiium.monban.watch | grep -E "last exit code|runs"
```

見ること：

- `last exit code = 0`。門番が動いているので、知らせは来ない
- 0 でなければ、`sudo -u _monban /usr/local/libexec/monban/monban watch -state-dir /var/db/monban -pushover /var/db/monban/secret/pushover.json` を `cd /` してから打つと、理由が画面に出る

## 試す（任意）

門番を合図で止めて（`monban stop` だと印が残って、知らせない）、見張りを今すぐ動かす：

```sh
sudo launchctl bootout system/io.github.mediiiiium.monban
sudo launchctl kickstart system/io.github.mediiiiium.monban.watch
```

「monban 止まっている」が届く。戻す：

```sh
sudo launchctl bootstrap system /Library/LaunchDaemons/io.github.mediiiiium.monban.plist
sleep 3; sudo launchctl kickstart system/io.github.mediiiiium.monban.watch
```

「monban 戻った」が届く。
