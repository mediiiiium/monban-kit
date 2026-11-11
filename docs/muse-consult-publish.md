# Muse への相談：monban-kit のリポジトリを公開するか

作成日: 2026-11-12
読んでほしいもの：[README.md](../README.md)、[v1-plan.md](v1-plan.md) の「これで防げないこと」、`scripts/install.sh`
前回の相談と回答：[muse-consult-production-check.md](muse-consult-production-check.md)、[muse-consult-production-check-response.md](muse-consult-production-check-response.md)

## 1. いまの状況

- 門番 v1 は10段すべて作り、10段目で本番の形の確かめも済んだ。高リスクの秘密はまだ扱っていない（ダミーの `MONBAN_TEST` だけ）
- この経緯を、note（体験の記事）と Zenn（作りの記事）に書いている。Zenn には要所のコードを短く載せたい（相手の uid の確かめ、`exec` の前にソケットを閉じるところ、など）
- リポジトリ `mediiiiium/monban-kit` は今は非公開。Zenn の参考にリンクを貼るなら、公開が要る
- あとで公開すると、確かめをもう一度やり直すことになるので、Zenn と一緒に済ませたい

## 2. 先にざっと見たこと（Claude Code が grep で見た範囲）

- 追跡しているファイルは45、コミットは37
- コミットの作者は GitHub の noreply のアドレスだけ
- 本人のホームのパスや個人のメールは、今のファイルにも履歴にも見当たらない（`/Users/` の当たりは `install.sh` の `dscl . -create "/Users/$USER_NAME"` だけ）
- 履歴に出る個人寄りの値は、本人の uid（501）と、`_monban` の uid（401）。launchd のラベルは `io.github.mediiiiium.monban`
- Pushover の token・user は、コードとテストでは欄の名前とダミー（`TOK`・`USR`）だけ。実物は `/var/db/monban/secret/pushover.json` にあり、追跡していない
- 合言葉（アクセストークン）・承認の番号・`ps eww` の生の出力は、docs に載せていない決まりで書いてきた。ただし全文を人の目では読み直していない
- LICENSE のファイルはない（相談の時点。回答を受けて MIT を足した）
- docs には、Muse との相談と回答が9往復ぶん（`muse-consult-*`）と、メモが2本（`muse-note-*`）ある。`mac-autofill-setup.md` は門番と関係のない、手元の Mac の自動入力の設定の記録

## 3. 聞きたいこと

### 3-1. 公開してよいか

- 防犯の道具を、専門家ではない個人が公開することになる。読んだ人が実際に使う前提になる
- README には「v1 は高リスクの秘密を扱わない。ターミナルの承認は事故防止」とすでに書いてある。これで足りるか、公開するなら、もっと目立つ形（冒頭の注意書きなど）が要るか
- 公開しない方がよい理由があれば、それも聞きたい（例：守りの穴の一覧を、攻める側に渡すことになる、など）

### 3-2. 公開から外すもの

- `muse-consult-*` と `muse-note-*` は残してよいか。決めた理由の記録として価値がある一方、Muse とのやり取りそのものを外に出すことになる
- `mac-autofill-setup.md` は外すつもり。ほかに外した方がよいものはあるか
- 外すとき、履歴ごと消す（書き換えて force push）か、今のファイルから消すだけでよいか

### 3-3. 公開の前に確かめること

- 2. の grep のほかに、見ておくべきものはあるか（例：uid やラベルを出してよいか、テストの値、`.gitignore` の漏れ）
- reviewer（Claude Code のレビュー用エージェント）に、履歴まで含めた機密の確かめをさせるつもり。見る観点で足りないものはあるか

### 3-4. LICENSE と、受け付けるもの

- LICENSE は何がよいか（MIT など）。保証しないことを明示したい
- issue や pull request は受け付けるか。受け付けないなら、README にどう書くか

### 3-5. Zenn に載せるコードの範囲

- 要所だけ短く載せる案。公開するなら、載せたところにリポジトリの該当行へのリンクを付ける
- 載せない方がよいところはあるか（例：署名の確かめの細部、承認の番号の作り方）
