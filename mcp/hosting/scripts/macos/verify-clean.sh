#!/bin/sh
set -eu
operation=$1
customer=$2
serial=$3
[ "$(id -u)" = 0 ] && [ "$(uname -s)" = Darwin ] || exit 10
case "$operation" in *[!a-f0-9-]*|'') exit 11;; esac
[ ${#operation} -eq 36 ] || exit 12
actual=$(/usr/sbin/ioreg -rd1 -c IOPlatformExpertDevice | /usr/bin/awk -F '"' '/IOPlatformSerialNumber/{print $(NF-1)}')
[ "$actual" = "$serial" ] || exit 13
receipt="/var/root/.apteva-hosting/receipts/$operation.plist"
[ ! -L "$receipt" ] && [ "$(/usr/bin/stat -f '%Su:%Lp' "$receipt")" = root:600 ] || exit 14
get(){ /usr/bin/plutil -extract "$1" raw -o - "$receipt"; }
[ "$(get OperationID)" = "$operation" ] && [ "$(get Serial)" = "$serial" ] || exit 15
[ "$(get RecoveryEnrollmentVerified)" = true ] || exit 16
current=$(/usr/sbin/diskutil info -plist /System/Volumes/Data | /usr/bin/plutil -extract VolumeUUID raw -o - -)
[ -n "$(get PreviousDataVolumeUUID)" ] && [ "$current" != "$(get PreviousDataVolumeUUID)" ] || exit 17
[ "$current" = "$(get NewDataVolumeUUID)" ] || exit 18
[ "$(/usr/bin/sw_vers -productVersion)" = "$(get MacOSVersion)" ] || exit 19
# Explicit exit: set -e ignores a command that starts with `!`.
if /usr/bin/dscl . -list /Users | /usr/bin/grep -q '^hst_'; then exit 22; fi
for home in /Users/hst_*; do [ ! -e "$home" ] || exit 20; done
for key in /etc/ssh/apteva-hosting-keys/hst_*; do [ ! -e "$key" ] || exit 21; done
/usr/sbin/sshd -t || exit 23
printf 'HOSTING_OK:%s:verify_clean\n' "$operation"
