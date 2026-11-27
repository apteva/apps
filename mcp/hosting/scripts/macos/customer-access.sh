#!/bin/sh
# Apteva Hosting customer access for macOS. Uploaded by Hosting, copied to a
# root-owned directory and hash-checked before it runs as root.
# Args: step customer operation serial keys_b64 management
#   serial "-" skips the hardware serial check (on-demand machines).
#   management "-" skips the management-account checks (on-demand machines).
set -eu
step=$1
customer=$2
operation=$3
serial=$4
keys_b64=$5
management=$6
[ "$(id -u)" = 0 ] && [ "$(uname -s)" = Darwin ] || exit 10
case "$customer" in hst_[a-f0-9]*) ;; *) exit 11;; esac
[ ${#customer} -eq 24 ] || exit 12
[ "$customer" != "$management" ] || exit 13
if [ "$serial" != "-" ]; then
 actual_serial=$(/usr/sbin/ioreg -rd1 -c IOPlatformExpertDevice | /usr/bin/awk -F '"' '/IOPlatformSerialNumber/{print $(NF-1)}')
 [ "$actual_serial" = "$serial" ] || exit 14
fi
if [ "$management" != "-" ]; then
 /usr/bin/id "$management" >/dev/null
 [ "$(/usr/bin/id -u "$management")" != 0 ] || exit 15
fi
# Keep authorized keys outside customer-writable home directories. sshd reads
# AuthorizedKeysFile as the logging-in user, so files are root-owned 0644.
key_dir=/etc/ssh/apteva-hosting-keys
[ ! -L "$key_dir" ] || exit 16
/usr/bin/install -d -o root -g wheel -m 755 "$key_dir"
key_file="$key_dir/$customer"
[ ! -L "$key_file" ] || exit 17
profile=/etc/ssh/sshd_config.d/100-apteva-hosting.conf
routed() {
 /usr/sbin/sshd -T -C "user=$customer,host=localhost,addr=127.0.0.1" | /usr/bin/grep -F 'authorizedkeysfile /etc/ssh/apteva-hosting-keys/%u' >/dev/null
}
case "$step" in
 prepare)
  # Adds a drop-in that applies only to hst_* accounts. The management SSH
  # configuration is never replaced; a profile that fails sshd -t is removed.
  /usr/bin/grep -Eq '^[[:space:]]*Include[[:space:]]+/etc/ssh/sshd_config.d/' /etc/ssh/sshd_config || exit 24
  [ ! -L "$profile" ] || exit 25
  tmp="$profile.$operation.tmp"
  (umask 022; printf '%s\n' \
   '# Managed by Apteva Hosting. Applies only to hosted customer accounts.' \
   'Match User hst_*' \
   '    AuthorizedKeysFile /etc/ssh/apteva-hosting-keys/%u' \
   '    PasswordAuthentication no' \
   '    KbdInteractiveAuthentication no' \
   '    PermitUserEnvironment no' \
   'Match all' > "$tmp")
  /usr/sbin/chown root:wheel "$tmp"; /bin/mv -f "$tmp" "$profile"
  if ! /usr/sbin/sshd -t; then /bin/rm -f "$profile"; exit 26; fi
  routed
  ;;
 preflight)
  [ "$management" = "-" ] || /usr/bin/dscl . -read "/Users/$management" NFSHomeDirectory >/dev/null
  /usr/sbin/sshd -t
  routed
  ;;
 grant)
  [ -n "$keys_b64" ] && [ "$keys_b64" != "-" ] || exit 20
  if ! /usr/bin/id "$customer" >/dev/null 2>&1; then
   password=$(/usr/bin/openssl rand -base64 36)
   /usr/sbin/sysadminctl -addUser "$customer" -fullName 'Hosted customer' -password "$password" >/dev/null 2>&1
   unset password
  fi
  uid=$(/usr/bin/id -u "$customer")
  [ "$uid" -ge 501 ] || exit 21
  /usr/bin/dscl . -read "/Users/$customer" NFSHomeDirectory | /usr/bin/grep -Fx "NFSHomeDirectory: /Users/$customer" >/dev/null
  /usr/bin/dscl . -delete "/Users/$customer" AuthenticationAuthority ';DisabledUser;' >/dev/null 2>&1 || true
  tmp="$key_dir/.${operation}.tmp"
  [ ! -e "$tmp" ] || /bin/rm -f "$tmp"
  (umask 077; printf '%s' "$keys_b64" | /usr/bin/base64 -D > "$tmp")
  [ -s "$tmp" ] || exit 22
  /usr/bin/ssh-keygen -l -f "$tmp" >/dev/null
  /usr/sbin/chown root:wheel "$tmp"; /bin/chmod 644 "$tmp"; /bin/mv -f "$tmp" "$key_file"
  routed
  if /usr/bin/dscl . -read /Groups/com.apple.access_ssh >/dev/null 2>&1; then /usr/sbin/dseditgroup -o edit -a "$customer" -t user com.apple.access_ssh; fi
  ;;
 revoke)
  if /usr/bin/id "$customer" >/dev/null 2>&1; then
   /usr/bin/dscl . -read "/Users/$customer" AuthenticationAuthority 2>/dev/null | /usr/bin/grep -q DisabledUser || \
    /usr/bin/dscl . -append "/Users/$customer" AuthenticationAuthority ';DisabledUser;'
   /usr/bin/pkill -KILL -u "$customer" || [ $? -eq 1 ]
   /usr/sbin/dseditgroup -o edit -d "$customer" -t user com.apple.access_ssh >/dev/null 2>&1 || true
   /usr/sbin/dseditgroup -o edit -d "$customer" -t user com.apple.access_screensharing >/dev/null 2>&1 || true
  fi
  /bin/rm -f "$key_file"
  ;;
 verify_access)
  /usr/bin/id "$customer" >/dev/null
  # Checks are explicit exits: set -e ignores `! cmd` and all but the last
  # command of `a && b`.
  [ -s "$key_file" ] || exit 32
  [ "$(/usr/bin/stat -f '%Su:%Lp' "$key_file")" = root:644 ] || exit 32
  if /usr/bin/dscl . -read "/Users/$customer" AuthenticationAuthority 2>/dev/null | /usr/bin/grep -q DisabledUser; then exit 31; fi
  /usr/sbin/sshd -t
  routed
  ;;
 verify_revoked)
  [ ! -e "$key_file" ]
  if /usr/bin/id "$customer" >/dev/null 2>&1; then
   /usr/bin/dscl . -read "/Users/$customer" AuthenticationAuthority | /usr/bin/grep -q DisabledUser
   if /usr/bin/pgrep -u "$customer" >/dev/null; then exit 33; fi
  fi
  ;;
 check_clean)
  # Used by inspect: no hosted customer account, home or key may survive.
  if /usr/bin/dscl . -list /Users | /usr/bin/grep -q '^hst_'; then exit 34; fi
  for home in /Users/hst_*; do [ ! -e "$home" ] || exit 27; done
  for key in "$key_dir"/hst_*; do [ ! -e "$key" ] || exit 28; done
  /usr/sbin/sshd -t
  ;;
 *) exit 30;;
esac
printf 'HOSTING_OK:%s:%s\n' "$operation" "$step"
