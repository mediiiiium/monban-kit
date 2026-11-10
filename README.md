# monban-kit

monban（門番）= gatekeeper。鍵を誰に・いつ渡すかを門番が決める、自分用のパスワード管理の構成。自分の複数デバイス、スクリプトやCI、AIエージェントまでを同じ門番が仲介する。いずれOSSとして公開する予定。

- 金庫：Bitwarden（人用は個人金庫、エージェント用は Secrets Manager）
- 手元のエージェント：門番（許可リスト・承認・キルスイッチ）経由
- 離れた場所のエージェント：Bitwarden Agent Access

設計と経緯は [docs/design.md](docs/design.md)。

## いまの段階
- Bitwarden を1週間試す（iPhone の「パスワード」アプリから数件だけ移す）
- Secrets Manager の秘密を1つ、Claude Code と Codex の両方から使えるか手で通す（手順：[docs/first-secret-trial.md](docs/first-secret-trial.md)）
- 結果を見て、門番を書くか、`bws` だけで足りるかを決める

## 注意
パスワードの書き出しファイル（CSV など）や合言葉（トークン）は、このリポジトリに置かない。`.gitignore` で弾いているが、置かないのが前提。
