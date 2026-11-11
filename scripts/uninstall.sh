#!/bin/sh
# 門番（monban）を消す（9段目）。
#
#   sudo scripts/uninstall.sh [--remove-logs]
#   scripts/uninstall.sh -n [--remove-logs]   （打つコマンドを出すだけ。何も変えない）
#
# - 中身を読んでから、人が sudo で動かす。エージェントには動かさせない
# - 消す前に、持ち主が想定どおり（_monban か root）かを見る。違えば、そこで止まる
# - 合言葉のファイルは消える。合言葉そのものは生きているので、要るなら失効させる（最後に手順を出す）
# - ログ（/var/log/monban）は、--remove-logs を付けなければ残す
# - 番号の記録（/usr/local/etc/monban/ids）は残す。入れ直すときに同じ番号を使う

set -eu

LABEL=io.github.mediiiiium.monban
USER_NAME=_monban
GROUP_NAME=_monbanusers
LIBEXEC=/usr/local/libexec/monban
BIN_LINK=/usr/local/bin/monban
ETC=/usr/local/etc/monban
IDS=$ETC/ids
STATE=/var/db/monban
RUNDIR=/var/db/monban-run
LOGDIR=/var/log/monban
PLIST=/Library/LaunchDaemons/$LABEL.plist

DRY=0
LOGS=0
for a in "$@"; do
	case "$a" in
	-n) DRY=1 ;;
	--remove-logs) LOGS=1 ;;
	*)
		echo "使い方: sudo $0 [-n] [--remove-logs]" >&2
		exit 2
		;;
	esac
done

say() { printf '\n==> %s\n' "$*"; }
die() {
	printf '\n止めた：%s\n' "$*" >&2
	exit 1
}
run() {
	printf '  $ %s\n' "$*"
	if [ "$DRY" = 0 ]; then
		"$@"
	fi
}
# owned <パス> <uid>…：パスの持ち主が、挙げた uid のどれかであること。シンボリックリンクは不可。
owned() {
	p=$1
	shift
	[ ! -L "$p" ] || die "シンボリックリンクになっている（消さない）: $p"
	u=$(stat -f '%u' "$p")
	for w in "$@"; do
		[ "$u" = "$w" ] && return 0
	done
	die "持ち主が想定と違う（uid ${u}）ので消さない: $p"
}

say "0. 前の確かめ"
[ "$(uname -s)" = Darwin ] || die "macOS ではない"
if [ "$DRY" = 0 ]; then
	[ "$(id -u)" = 0 ] || die "sudo で動かしてください"
fi
N=
if [ -f "$IDS" ]; then
	N=$(sed -n 's/^id=\([0-9][0-9]*\)$/\1/p' "$IDS")
fi
HAVE_USER=$(dscl . -read "/Users/$USER_NAME" UniqueID 2>/dev/null | awk '{print $2}')
if [ -n "$HAVE_USER" ] && [ -n "$N" ] && [ "$HAVE_USER" != "$N" ]; then
	die "$USER_NAME の uid（${HAVE_USER}）が記録（${N}）と違う。入れたものと違うかもしれないので止める"
fi
[ -n "$N" ] || N=$HAVE_USER
HAVE_GROUP=$(dscl . -read "/Groups/$GROUP_NAME" PrimaryGroupID 2>/dev/null | awk '{print $2}')
if [ -n "$HAVE_GROUP" ] && [ -n "$N" ] && [ "$HAVE_GROUP" != "$N" ]; then
	die "$GROUP_NAME の gid（${HAVE_GROUP}）が記録（${N}）と違う。入れたものと違うかもしれないので止める"
fi
[ -n "$N" ] || echo "  番号の記録も $USER_NAME もない（消すものは root の持ち物だけのはず）"
echo "  番号：${N:-なし}"

say "1. launchd から外す（動いていれば、合図で止まり、停止の行を書く。止めた印は置かない）"
if /bin/launchctl print "system/$LABEL" >/dev/null 2>&1; then
	run /bin/launchctl bootout "system/$LABEL"
else
	echo "  入っていない"
fi
if [ -e "$PLIST" ] || [ -L "$PLIST" ]; then
	owned "$PLIST" 0
	run rm "$PLIST"
fi

say "2. バイナリと許可リストを消す（番号の記録 $IDS は残す）"
if [ -L "$BIN_LINK" ]; then
	[ "$(readlink "$BIN_LINK")" = "$LIBEXEC/monban" ] || die "$BIN_LINK が門番のバイナリを指していない（消さない）"
	run rm "$BIN_LINK"
elif [ -e "$BIN_LINK" ]; then
	die "$BIN_LINK がシンボリックリンクではない（門番が置いたものではないので消さない）"
fi
if [ -e "$LIBEXEC" ] || [ -L "$LIBEXEC" ]; then
	owned "$LIBEXEC" 0
	run rm -rf "$LIBEXEC"
fi
if [ -e "$ETC/allowlist.json" ] || [ -L "$ETC/allowlist.json" ]; then
	owned "$ETC/allowlist.json" 0
	run rm "$ETC/allowlist.json"
fi

say "3. 状態のディレクトリ（合言葉・Pushover・止めた印）と、ソケットの置き場を消す"
for p in "$STATE" "$RUNDIR"; do
	if [ -e "$p" ] || [ -L "$p" ]; then
		[ -n "$N" ] || die "番号がわからないので、$p の持ち主を確かめられない"
		owned "$p" "$N"
		run rm -rf "$p"
	fi
done

say "4. ログ"
if [ -e "$LOGDIR" ] || [ -L "$LOGDIR" ]; then
	if [ "$LOGS" = 1 ]; then
		[ -n "$N" ] || die "番号がわからないので、$LOGDIR の持ち主を確かめられない"
		owned "$LOGDIR" "$N"
		run rm -rf "$LOGDIR"
	else
		echo "  残す: ${LOGDIR}（消すなら --remove-logs。入れ直すと、同じ連鎖に続けて書く）"
	fi
else
	echo "  ない"
fi

say "5. ユーザー $USER_NAME とグループ $GROUP_NAME を消す"
if [ -n "$HAVE_USER" ]; then
	run dscl . -delete "/Users/$USER_NAME"
else
	echo "  ユーザーはない"
fi
if [ -n "$HAVE_GROUP" ]; then
	run dscl . -delete "/Groups/$GROUP_NAME"
else
	echo "  グループはない"
fi

say "6. 消し終わった"
cat <<EOF

  合言葉のファイルは消えたが、合言葉（アクセストークン）そのものは生きている。
  もう使わないなら、Bitwarden のウェブの画面で失効させる：
    Secrets Manager →「Machine accounts」→ 門番のマシンアカウント →「Access tokens」→「Revoke access token」
  失効させても、合言葉でログイン済みの相手は、誰であっても最大1時間は読み続けられる。
EOF
