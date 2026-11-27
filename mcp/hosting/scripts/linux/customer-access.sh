#!/bin/sh
# Apteva Hosting customer access for Linux. Uploaded by Hosting, copied to a
# root-owned directory and hash-checked before it runs as root.
# Args: step customer operation serial keys_b64 management
#   serial "-" skips the hardware serial check.
#   management "-" skips the management-account checks.
set -eu
step=$1
customer=$2
operation=$3
serial=$4
keys_b64=$5
management=$6
[ "$(id -u)" = 0 ] && [ "$(uname -s)" = Linux ] || exit 10
case "$customer" in hst_[a-f0-9]*) ;; *) exit 11;; esac
[ ${#customer} -eq 24 ] || exit 12
[ "$customer" != "$management" ] || exit 13
if [ "$serial" != "-" ]; then
 [ "$(cat /sys/class/dmi/id/product_serial 2>/dev/null || true)" = "$serial" ] || exit 14
fi
if [ "$management" != "-" ]; then
 id "$management" >/dev/null
 [ "$(id -u "$management")" != 0 ] || exit 15
fi
# sshd reads AuthorizedKeysFile as the logging-in user, so files are
# root-owned 0644 in a root-owned directory the customer cannot write.
key_dir=/etc/ssh/apteva-hosting-keys
[ ! -L "$key_dir" ] || exit 16
install -d -o root -g root -m 755 "$key_dir"
key_file="$key_dir/$customer"
[ ! -L "$key_file" ] || exit 17
profile=/etc/ssh/sshd_config.d/100-apteva-hosting.conf
sshd_bin=$(command -v sshd || echo /usr/sbin/sshd)
routed() {
 "$sshd_bin" -T -C "user=$customer,host=localhost,addr=127.0.0.1" | grep -F 'authorizedkeysfile /etc/ssh/apteva-hosting-keys/%u' >/dev/null
}
reload_sshd() {
 systemctl reload ssh 2>/dev/null || systemctl reload sshd 2>/dev/null || service ssh reload 2>/dev/null || service sshd reload
}
# usermod --lock prefixes the shadow hash with "!", which sshd treats as locked.
locked() {
 case "$(getent shadow "$customer" | cut -d: -f2)" in '!'*) return 0;; *) return 1;; esac
}
case "$step" in
 prepare)
  grep -Eq '^[[:space:]]*Include[[:space:]]+/etc/ssh/sshd_config.d/' /etc/ssh/sshd_config || exit 24
  [ ! -L "$profile" ] || exit 25
  install -d -o root -g root -m 755 /etc/ssh/sshd_config.d
  tmp="$profile.$operation.tmp"
  (umask 022; printf '%s\n' \
   '# Managed by Apteva Hosting. Applies only to hosted customer accounts.' \
   'Match User hst_*' \
   '    AuthorizedKeysFile /etc/ssh/apteva-hosting-keys/%u' \
   '    PasswordAuthentication no' \
   '    KbdInteractiveAuthentication no' \
   '    PermitUserEnvironment no' \
   'Match all' > "$tmp")
  chown root:root "$tmp"; mv -f "$tmp" "$profile"
  if ! "$sshd_bin" -t; then rm -f "$profile"; exit 26; fi
  reload_sshd
  routed
  ;;
 preflight)
  "$sshd_bin" -t
  routed
  ;;
 grant)
  [ -n "$keys_b64" ] && [ "$keys_b64" != "-" ] || exit 20
  if ! id "$customer" >/dev/null 2>&1; then
   useradd --create-home --shell /bin/bash --comment 'Hosted customer' "$customer"
  fi
  uid=$(id -u "$customer")
  [ "$uid" -ge 1000 ] || exit 21
  usermod --shell /bin/bash "$customer"
  # Replaces useradd's "!" (or a revoke's "!*") with "*": not locked for key
  # login, and no password ever matches it.
  usermod --password '*' "$customer"
  tmp="$key_dir/.${operation}.tmp"
  [ ! -e "$tmp" ] || rm -f "$tmp"
  (umask 077; printf '%s' "$keys_b64" | base64 -d > "$tmp")
  [ -s "$tmp" ] || exit 22
  ssh-keygen -l -f "$tmp" >/dev/null
  chown root:root "$tmp"; chmod 644 "$tmp"; mv -f "$tmp" "$key_file"
  routed
  ;;
 revoke)
  if id "$customer" >/dev/null 2>&1; then
   usermod --lock --shell /usr/sbin/nologin "$customer"
   pkill -KILL -u "$customer" || [ $? -eq 1 ]
  fi
  rm -f "$key_file"
  ;;
 verify_access)
  id "$customer" >/dev/null
  # Checks are explicit exits: set -e ignores `! cmd` and all but the last
  # command of `a && b`.
  [ -s "$key_file" ] || exit 32
  [ "$(stat -c '%U:%a' "$key_file")" = root:644 ] || exit 32
  if locked; then exit 31; fi
  [ "$(getent passwd "$customer" | cut -d: -f7)" = /bin/bash ]
  "$sshd_bin" -t
  routed
  ;;
 verify_revoked)
  [ ! -e "$key_file" ]
  if id "$customer" >/dev/null 2>&1; then
   locked
   if pgrep -u "$customer" >/dev/null; then exit 33; fi
  fi
  ;;
 check_clean)
  if getent passwd | cut -d: -f1 | grep -q '^hst_'; then exit 34; fi
  for home in /home/hst_*; do [ ! -e "$home" ] || exit 27; done
  for key in "$key_dir"/hst_*; do [ ! -e "$key" ] || exit 28; done
  "$sshd_bin" -t
  ;;
 *) exit 30;;
esac
printf 'HOSTING_OK:%s:%s\n' "$operation" "$step"
