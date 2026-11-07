package main

// Opt-in, host-wide Ubuntu SSH-session optimization. Add only pam_motd's
// documented noupdate option; retain every authentication/account/session rule.
// This does not disable PAM, remove login messages or restart sshd.
func automationMOTDCommand(file string) string {
	return `set -eu
 file=` + quoteShellArg(file) + `
 test -f "$file"; test ! -L "$file"; test ! -L "${file}.apteva-before-motd"
 exec 7<"$file"; flock -x 7
 original=$(mktemp "${file}.apteva-original.XXXXXX")
 updated=$(mktemp "${file}.apteva-updated.XXXXXX")
 trap 'rm -f -- "$original" "$updated"' EXIT HUP INT TERM
 cp -p -- "$file" "$original"
 awk '
 /^[[:space:]]*session[[:space:]]+optional[[:space:]]+pam_motd[.]so([[:space:]]|$)/ {
  matched++; line=$0; sub(/[[:space:]]+#.*/, "", line)
  if (line !~ /(^|[[:space:]])noupdate([[:space:]]|$)/) {
   if (match($0, /[[:space:]]+#/)) $0=substr($0,1,RSTART-1) " noupdate" substr($0,RSTART)
   else $0=$0 " noupdate"
  }
 }
 { print }
 END { if (!matched) exit 1 }
 ' "$original" > "$updated"
 if ! cmp -s "$original" "$updated"; then
  chmod --reference="$original" "$updated"
  chown --reference="$original" "$updated"
  if test ! -e "${file}.apteva-before-motd"; then cp -p -- "$original" "${file}.apteva-before-motd"; fi
  cmp -s "$original" "$file"
  mv -f -- "$updated" "$file"
 fi
 `
}
