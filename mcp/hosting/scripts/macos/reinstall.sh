#!/bin/sh
# Reference Apple-silicon erase/install adapter. A validated recovery enrollment must
# preserve the operation intent off-device and restore management access plus a receipt.
set -eu
mode=reinstall
operation=$1
customer=$2
serial=$3
[ "$(id -u)" = 0 ] && [ "$(uname -s)" = Darwin ] || exit 10
case "$operation" in *[!a-f0-9-]*|'') exit 11;; esac
[ ${#operation} -eq 36 ] || exit 12
actual=$(/usr/sbin/ioreg -rd1 -c IOPlatformExpertDevice | /usr/bin/awk -F '"' '/IOPlatformSerialNumber/{print $(NF-1)}')
[ "$actual" = "$serial" ] || exit 13
root=/var/root/.apteva-hosting
profile="$root/recovery.plist"
[ ! -L "$profile" ] && [ "$(/usr/bin/stat -f '%Su:%Lp' "$profile")" = root:600 ] || exit 14
get(){ /usr/bin/plutil -extract "$1" raw -o - "$profile"; }
[ "$(get RecoveryValidated)" = true ] || exit 15
installer=$(get InstallerPath)
management=$(get ManagementUser)
password_file=$(get PasswordFile)
# The password never enters MCP inputs, output, source control, or the process argument list.
[ ! -L "$password_file" ] && [ "$(/usr/bin/stat -f '%Su:%Lp' "$password_file")" = root:600 ] || exit 16
[ -x "$installer/Contents/Resources/startosinstall" ] || exit 17
/usr/bin/codesign --verify --deep --strict "$installer" || exit 20
/usr/sbin/spctl --assess --type execute "$installer" || exit 21
/usr/sbin/sysadminctl -secureTokenStatus "$management" 2>&1 | /usr/bin/grep -q ENABLED || exit 22
handoff=/usr/local/libexec/apteva-hosting/recovery-handoff
[ ! -L "$handoff" ] && [ "$(/usr/bin/stat -f '%Su:%Lp' "$handoff")" = root:700 ] || exit 18
[ "$(/usr/bin/shasum -a 256 "$handoff" | /usr/bin/awk '{print $1}')" = "$(get RecoveryHandoffSHA256)" ] || exit 19
# This approved helper must durably acknowledge the off-device recovery intent before erase.
volume=$(/usr/sbin/diskutil info -plist /System/Volumes/Data | /usr/bin/plutil -extract VolumeUUID raw -o - -)
"$handoff" "$operation" "$serial" "$volume" || { echo "recovery-handoff exited $?" >&2; exit 23; }
job="$root/reset-$operation.sh"
if [ ! -e "$job" ]; then
 umask 077
 # Values come only from an operator-owned plist; shell quoting is supplied by printf %q via bash.
 /bin/bash -c 'printf "#!/bin/bash\nset -eu\nsleep 10\nexec %q --eraseinstall --agreetolicense --forcequitapps --user %q --stdinpass < %q\n" "$1" "$2" "$3"' _ "$installer/Contents/Resources/startosinstall" "$management" "$password_file" > "$job"
 /bin/chmod 700 "$job"
fi
label="com.apteva.hosting.reset.$operation"
if ! /bin/launchctl list "$label" >/dev/null 2>&1; then
 /bin/launchctl submit -l "$label" -o "$root/reset-$operation.log" -e "$root/reset-$operation.log" -- /bin/bash "$job"
fi
# This acknowledges scheduling only. The separate verify_clean step is mandatory.
printf 'HOSTING_OK:%s:%s\n' "$operation" "$mode"
