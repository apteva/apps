# Instances 0.17.0

The native backend can now install host profiles: named sets of OS packages a
release needs on its target. A release declares them in
`runtime.package.host_profiles`; Server forwards the placement's approval as
`approved_host_profiles`.

- `inspect-target` reports every profile this backend knows as `present`,
  `installable` (missing, apt is available) or `unsupported`. The probe now also
  reads os-release `ID`/`ID_LIKE` and whether any font file is installed.
- `validate-target` and `prepare` refuse a declared profile that is missing and
  not approved, unknown, or uninstallable on the target.
- The prepare job installs approved missing profiles with
  `apt-get install --no-install-recommends` (lock timeout 5 min; the job budget
  grows by 15 min), then re-probes and fails unless every profile is satisfied.
  apt's output is kept in `/var/log/apteva-host-setup.log` on the target.
  Packages are never removed: the host is shared.
- Capabilities advertise `host_profiles: [chrome-headless]`.

`chrome-headless` covers the X11/NSS/ATK/GBM/ALSA libraries headless Chrome
loads plus fontconfig, DejaVu and Liberation fonts. Debian 12/13 and Ubuntu
22.04/24.04 package names (including the `t64` renames) are chosen at install
time.
