#!/bin/sh
# 門番（monban）を、本番の形で入れる（9段目）。
#
#   sudo scripts/install.sh <monban のバイナリ> <bws のバイナリ> <許可リスト>
#   scripts/install.sh -n <monban のバイナリ> <bws のバイナリ> <許可リスト>   （打つコマンドを出すだけ。何も変えない）
#
# - 中身を読んでから、人が sudo で動かす。エージェントには動かさせない
# - 各ステップを画面に出しながら進む。既にあるユーザー・置き場が想定と違えば、上書きせずに止まる
# - 合言葉と Pushover のキーには触らない（入れ方は最後に出す。人が新しいウィンドウで入れる）
# - 入れるのは、何もないところか、scripts/uninstall.sh の後だけ（ログと番号の記録は残っていてよい）
# - 門番を起動するのは、合言葉と Pushover を入れた後。このスクリプトは起動しない

set -eu

LABEL=io.github.mediiiiium.monban
USER_NAME=_monban
GROUP_NAME=_monbanusers
LIBEXEC=/usr/local/libexec/monban
BIN_LINK=/usr/local/bin/monban
ETC=/usr/local/etc/monban
IDS=$ETC/ids
STATE=/var/db/monban
SECRET=$STATE/secret
RUNDIR=/var/db/monban-run
LOGDIR=/var/log/monban
PLIST=/Library/LaunchDaemons/$LABEL.plist
# bws は Bitwarden Inc の Developer ID で署名されていること（門番の bws.go と同じ条件）。
BWS_REQ='anchor apple generic and identifier "bws" and certificate leaf[subject.OU] = "LTZ2PFU5D6"'

DRY=0
if [ "${1:-}" = "-n" ]; then
	DRY=1
	shift
fi
if [ $# -ne 3 ]; then
	echo "使い方: sudo $0 [-n] <monban のバイナリ> <bws のバイナリ> <許可リスト>" >&2
	exit 2
fi
SRC_MONBAN=$1
SRC_BWS=$2
SRC_ALLOW=$3

say() { printf '\n==> %s\n' "$*"; }
die() {
	printf '\n止めた：%s\n' "$*" >&2
	printf '（ここまでに作ったものは、そのまま残っている。scripts/uninstall.sh で消せる）\n' >&2
	exit 1
}
# run は、打つコマンドを出してから打つ。-n のときは出すだけ。
run() {
	printf '  $ %s\n' "$*"
	if [ "$DRY" = 0 ]; then
		"$@"
	fi
}

# --- 0. 前の確かめ（何も変えない） ---

say "0. 前の確かめ"
[ "$(uname -s)" = Darwin ] || die "macOS ではない"
if [ "$DRY" = 0 ]; then
	[ "$(id -u)" = 0 ] || die "sudo で動かしてください"
	[ -n "${SUDO_UID:-}" ] && [ "$SUDO_UID" != 0 ] || die "root のシェルからではなく、本人のシェルから sudo で動かしてください（SUDO_UID で持ち主を決める）"
	OWNER_UID=$SUDO_UID
	OWNER_NAME=$SUDO_USER
else
	OWNER_UID=$(id -u)
	OWNER_NAME=$(id -un)
fi
echo "  門番に繋いでよい人：${OWNER_NAME}（uid ${OWNER_UID}）"

for f in "$SRC_MONBAN" "$SRC_BWS" "$SRC_ALLOW"; do
	[ -f "$f" ] && [ ! -L "$f" ] || die "普通のファイルではない（シンボリックリンクも不可）: $f"
done
/usr/bin/codesign --verify --strict "-R=$BWS_REQ" "$SRC_BWS" 2>/dev/null ||
	die "bws の署名が Bitwarden のものではない: $SRC_BWS"
echo "  bws の署名：Bitwarden（TeamIdentifier LTZ2PFU5D6）"
# root では動かさない（形だけを見る）。許可リストの中身は、門番が起動のときに確かめる。
/usr/bin/file -b "$SRC_MONBAN" | grep -q '^Mach-O' || die "monban のバイナリが Mac の実行ファイルではない: $SRC_MONBAN"

if /bin/launchctl print "system/$LABEL" >/dev/null 2>&1; then
	die "門番が launchd に入っている（先に scripts/uninstall.sh）"
fi
# 何もないはずの場所。
for p in "$PLIST" "$LIBEXEC" "$BIN_LINK" "$ETC/allowlist.json" "$STATE" "$RUNDIR"; do
	if [ -e "$p" ] || [ -L "$p" ]; then
		die "もうある（入れ直すなら、先に scripts/uninstall.sh）: $p"
	fi
done
# 上のディレクトリ（あるものだけ）。ユーザーの持ち物だと、同じ uid のエージェントが中身を差し替えられる。
for p in /usr/local /usr/local/bin /usr/local/libexec /usr/local/etc "$ETC" /var/db /var/log /Library/LaunchDaemons; do
	[ -d "$p" ] || continue
	[ "$(stat -f '%u' "$p")" = 0 ] || die "root の持ち物ではない（門番は、上のディレクトリを書き換えられる置き場を受け付けない）: $p"
	case "$(stat -f '%Lp' "$p")" in
	*[2367]? | *?[2367]) die "グループかほかの人が書き込める: $p" ;;
	esac
done

# --- 1. 番号（uid と gid）を決める ---

say "1. _monban の番号を決める（uid と、$GROUP_NAME の gid に同じ番号を使う）"
uid_of() { dscl . -read "/Users/$1" UniqueID 2>/dev/null | awk '{print $2}'; }
gid_of() { dscl . -read "/Groups/$1" PrimaryGroupID 2>/dev/null | awk '{print $2}'; }
uid_used() { dscl . -list /Users UniqueID | awk -v n="$1" '$2 == n {f=1} END {exit !f}'; }
gid_used() { dscl . -list /Groups PrimaryGroupID | awk -v n="$1" '$2 == n {f=1} END {exit !f}'; }

HAVE_USER=$(uid_of "$USER_NAME")
HAVE_GROUP=$(gid_of "$GROUP_NAME")
if [ -e "$IDS" ] || [ -L "$IDS" ]; then
	[ -f "$IDS" ] && [ ! -L "$IDS" ] && [ "$(stat -f '%u %Lp' "$IDS")" = "0 644" ] ||
		die "番号の記録が root の持ち物の 0644 の普通のファイルではない: $IDS"
	N=$(sed -n 's/^id=\([0-9][0-9]*\)$/\1/p' "$IDS")
	[ -n "$N" ] && [ "$N" -ge 400 ] && [ "$N" -le 499 ] || die "番号の記録が読めない: $IDS"
	echo "  前に入れたときの記録：${N}（${IDS}）"
else
	[ -z "$HAVE_USER" ] || die "$USER_NAME がもうある（uid ${HAVE_USER}）のに、番号の記録がない。誰が作ったかわからないので止める"
	[ -z "$HAVE_GROUP" ] || die "$GROUP_NAME がもうある（gid ${HAVE_GROUP}）のに、番号の記録がない。誰が作ったかわからないので止める"
	N=401
	while [ "$N" -le 499 ]; do
		if ! uid_used "$N" && ! gid_used "$N"; then
			break
		fi
		N=$((N + 1))
	done
	[ "$N" -le 499 ] || die "400番台に、uid と gid の両方が空いている番号がない"
	echo "  空いている番号：$N"
fi
# 名前と番号の両方の重なりを見る。
if [ -n "$HAVE_USER" ]; then
	[ "$HAVE_USER" = "$N" ] || die "$USER_NAME の uid が記録と違う（${HAVE_USER}、記録は ${N}）"
elif uid_used "$N"; then
	die "uid $N をほかのユーザーが使っている"
fi
if [ -n "$HAVE_GROUP" ]; then
	[ "$HAVE_GROUP" = "$N" ] || die "$GROUP_NAME の gid が記録と違う（${HAVE_GROUP}、記録は ${N}）"
elif gid_used "$N"; then
	die "gid $N をほかのグループが使っている"
fi

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

if [ ! -f "$IDS" ]; then
	run /usr/bin/install -d -o root -g wheel -m 0755 /usr/local/etc
	run /usr/bin/install -d -o root -g wheel -m 0755 "$ETC"
	printf 'id=%s\nuser=%s\ngroup=%s\n' "$N" "$USER_NAME" "$GROUP_NAME" >"$TMP/ids"
	run /usr/bin/install -o root -g wheel -m 0644 "$TMP/ids" "$IDS"
	echo "  番号を記録した（消すときと入れ直すときに使う。uninstall.sh も消さない）"
fi

# --- 2. グループとユーザー ---

say "2. グループ $GROUP_NAME とユーザー $USER_NAME を作り、$OWNER_NAME をグループに入れる"
if [ -z "$HAVE_GROUP" ]; then
	run dscl . -create "/Groups/$GROUP_NAME"
	run dscl . -create "/Groups/$GROUP_NAME" PrimaryGroupID "$N"
	run dscl . -create "/Groups/$GROUP_NAME" RealName "monban socket users"
	run dscl . -create "/Groups/$GROUP_NAME" Password '*'
else
	echo "  グループはもうある（gid ${N}、記録と同じ）"
fi
if [ -z "$HAVE_USER" ]; then
	run dscl . -create "/Users/$USER_NAME"
	run dscl . -create "/Users/$USER_NAME" UniqueID "$N"
	run dscl . -create "/Users/$USER_NAME" PrimaryGroupID "$N"
	run dscl . -create "/Users/$USER_NAME" UserShell /usr/bin/false
	run dscl . -create "/Users/$USER_NAME" NFSHomeDirectory /var/empty
	run dscl . -create "/Users/$USER_NAME" RealName "monban gatekeeper"
	run dscl . -create "/Users/$USER_NAME" Password '*'
	run dscl . -create "/Users/$USER_NAME" IsHidden 1
else
	echo "  ユーザーはもうある（uid ${N}、記録と同じ）"
fi
run dseditgroup -o edit -a "$OWNER_NAME" -t user "$GROUP_NAME"

# --- 3. バイナリ ---

say "3. monban と bws を $LIBEXEC に写す（root の持ち物の 0755）"
run /usr/bin/install -d -o root -g wheel -m 0755 /usr/local/libexec
run /usr/bin/install -d -o root -g wheel -m 0755 "$LIBEXEC"
run /usr/bin/install -o root -g wheel -m 0755 "$SRC_MONBAN" "$LIBEXEC/monban"
run /usr/bin/install -o root -g wheel -m 0755 "$SRC_BWS" "$LIBEXEC/bws"
if [ "$DRY" = 0 ]; then
	/usr/bin/codesign --verify --strict "-R=$BWS_REQ" "$LIBEXEC/bws" 2>/dev/null || die "写した bws の署名が合わない"
fi
run /usr/bin/install -d -o root -g wheel -m 0755 /usr/local/bin
run ln -s "$LIBEXEC/monban" "$BIN_LINK"

# --- 4. 許可リスト ---

say "4. 許可リストを $ETC/allowlist.json に写す（root の持ち物の 0644。直すたびに sudo が要る）"
run /usr/bin/install -o root -g wheel -m 0644 "$SRC_ALLOW" "$ETC/allowlist.json"

# --- 5. 状態とソケットの置き場 ---

say "5. 状態のディレクトリ（$USER_NAME だけ）と、ソケットの置き場（$GROUP_NAME も通れる）"
run /usr/bin/install -d -o "$N" -g "$N" -m 0700 "$STATE"
run /usr/bin/install -d -o "$N" -g "$N" -m 0700 "$SECRET"
run /usr/bin/install -d -o "$N" -g "$N" -m 0750 "$RUNDIR"

# --- 6. ログ ---

say "6. ログの置き場 $LOGDIR"
# ある・ないで分ける。あるなら、持ち主とモードが想定どおりかだけを見て、中身には触らない（連鎖はそのまま続く）。
want() { # want <パス> <uid> <gid> <モード>
	got=$(stat -f '%u %g %Lp' "$1")
	[ "$got" = "$2 $3 $4" ] || die "想定と違う（uid gid モード が「${got}」、想定は「$2 $3 $4」）: $1"
	[ ! -L "$1" ] || die "シンボリックリンクになっている: $1"
	echo "  もうある（想定どおり）: $1"
}
if [ -e "$LOGDIR" ] || [ -L "$LOGDIR" ]; then
	want "$LOGDIR" "$N" "$N" 750
else
	run /usr/bin/install -d -o "$N" -g "$N" -m 0750 "$LOGDIR"
fi
: >"$TMP/empty"
# audit.log：0640。グループ $GROUP_NAME の人（本人）が sudo なしで読める。
if [ -e "$LOGDIR/audit.log" ] || [ -L "$LOGDIR/audit.log" ]; then
	want "$LOGDIR/audit.log" "$N" "$N" 640
else
	run /usr/bin/install -o "$N" -g "$N" -m 0640 "$TMP/empty" "$LOGDIR/audit.log"
fi
# daemon.log：0600。承認の番号が出るので、$USER_NAME だけが読める。launchd に作らせず、先に作る。
if [ -e "$LOGDIR/daemon.log" ] || [ -L "$LOGDIR/daemon.log" ]; then
	want "$LOGDIR/daemon.log" "$N" "$N" 600
else
	run /usr/bin/install -o "$N" -g "$N" -m 0600 "$TMP/empty" "$LOGDIR/daemon.log"
fi

# --- 7. launchd ---

say "7. launchd の plist を $PLIST に置く（まだ起動しない）"
cat >"$TMP/plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>$LABEL</string>
	<key>ProgramArguments</key>
	<array>
		<string>$LIBEXEC/monban</string>
		<string>daemon</string>
		<string>-socket</string>
		<string>$RUNDIR/monban.sock</string>
		<string>-owner-uid</string>
		<string>$OWNER_UID</string>
		<string>-bws</string>
		<string>$LIBEXEC/bws</string>
		<string>-token-file</string>
		<string>$SECRET/token</string>
		<string>-pushover</string>
		<string>$SECRET/pushover.json</string>
		<string>-allowlist</string>
		<string>$ETC/allowlist.json</string>
		<string>-log</string>
		<string>$LOGDIR/audit.log</string>
		<string>-state-dir</string>
		<string>$STATE</string>
	</array>
	<key>UserName</key>
	<string>$USER_NAME</string>
	<key>GroupName</key>
	<string>$GROUP_NAME</string>
	<key>Umask</key>
	<integer>63</integer>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<dict>
		<key>SuccessfulExit</key>
		<false/>
	</dict>
	<key>StandardErrorPath</key>
	<string>$LOGDIR/daemon.log</string>
</dict>
</plist>
EOF
/usr/bin/plutil -lint -s "$TMP/plist" || die "plist の形が正しくない"
if [ "$DRY" = 1 ]; then
	sed 's/^/    /' "$TMP/plist"
fi
run /usr/bin/install -o root -g wheel -m 0644 "$TMP/plist" "$PLIST"

# --- 8. ここから先は人が打つ ---

say "8. 入れ終わった。ここから先は、エージェントのいない新しいウィンドウで打つ"
cat <<EOF

  a. 合言葉（アクセストークン）を入れる。画面に出さず、履歴にも残さない
     （cd / は、$USER_NAME が今のディレクトリに入れず警告が出るのを避けるため）：
       read -rs 'T?合言葉: '; echo
       (cd / && printf '%s\n' "\$T" | sudo -u $USER_NAME /bin/sh -c 'umask 077; cat > $SECRET/token')
       unset T

  b. Pushover のアプリのトークンとユーザーキーを入れる（本番の形では必須）：
       read -rs 'PT?Pushover のアプリのトークン: '; echo
       read -rs 'PU?Pushover のユーザーキー: '; echo
       (cd / && printf '{"token":"%s","user":"%s"}\n' "\$PT" "\$PU" | sudo -u $USER_NAME /bin/sh -c 'umask 077; cat > $SECRET/pushover.json')
       unset PT PU

  c. 起動する：
       sudo launchctl bootstrap system $PLIST

  d. 起動した後に、持ち主とモードを目で確かめる：
       sudo ls -la $SECRET $LOGDIR $RUNDIR
     見るところ：
       - token と pushover.json が $USER_NAME の 0600（-rw-------）
       - daemon.log が $USER_NAME の 0600（-rw-------）。承認の番号が出るので、ほかの人が読めてはいけない
       - audit.log が $USER_NAME:$GROUP_NAME の 0640（-rw-r-----）
       - monban.sock が $USER_NAME:$GROUP_NAME の 0660（srw-rw----）

  $OWNER_NAME が $GROUP_NAME に入ったのが効くのは、新しく開いたターミナルから。
  繋げないときは、ログインし直す。
EOF
