#!/usr/bin/env bash
# Build prebuilt packages for an Apteva app so installs skip the source build.
#
#   scripts/package-app.sh <app> <version> [--out DIR] [--write-manifest]
#
# Run it on the release commit: mcp/<app>/apteva.yaml at <version>, its
# runtime.source.ref = <app>/v<version>, and no runtime.artifacts block yet.
# Uncommitted changes under mcp/<app> are refused, because the packages are
# built from what git has committed (git archive), never from the work tree.
#
# For each of linux-amd64, linux-arm64, darwin-arm64, darwin-amd64 it writes
# <app>-v<version>-<os>-<arch>.tar.gz holding only:
#   bin                     the sidecar (CGO off, trimmed, stripped)
#   src/mcp/<app>/          apteva.yaml, the migrations folder, skill body
#                           files, every /ui/ file the manifest names and
#                           the icon
# plus SHA256SUMS and artifacts.yaml (the block for runtime.artifacts).
#
# --write-manifest inserts that block into mcp/<app>/apteva.yaml. Commit it as
# a follow-up commit and put the release tag on that commit: apteva-server
# accepts a package only when its apteva.yaml equals the installed manifest
# apart from `artifacts`.
#
# Then, from the tagged commit:
#   git push origin <app>/v<version>
#   gh release create <app>/v<version> --verify-tag --title "<app> v<version>" \
#     <out>/*.tar.gz <out>/SHA256SUMS
# and only after the assets are up, bump the registry. A manifest that lists a
# package for a platform never falls back to a source build there, so a
# missing asset breaks installs.
set -euo pipefail

usage() { echo "usage: $0 <app> <version> [--out DIR] [--write-manifest]" >&2; exit 2; }
[ $# -ge 2 ] || usage
APP="$1"; VERSION="${2#v}"; shift 2
OUT=""; WRITE_MANIFEST=0
while [ $# -gt 0 ]; do
  case "$1" in
    --out) OUT="${2:?}"; shift 2 ;;
    --write-manifest) WRITE_MANIFEST=1; shift ;;
    *) usage ;;
  esac
done

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
APP_REL="mcp/$APP"
APP_DIR="$ROOT/$APP_REL"
MANIFEST="$APP_DIR/apteva.yaml"
TAG="$APP/v$VERSION"
PLATFORMS="linux-amd64 linux-arm64 darwin-arm64 darwin-amd64"
OUT="${OUT:-$ROOT/dist/packages/$APP-v$VERSION}"
URL_BASE="https://github.com/apteva/apps/releases/download/$(printf %s "$TAG" | sed 's#/#%2F#g')"

die() { echo "package-app: $*" >&2; exit 1; }
command -v go >/dev/null || die "go not found"
command -v bun >/dev/null || die "bun not found"
[ -f "$MANIFEST" ] || die "$MANIFEST not found"

# Manifest facts, read with a real YAML parser.
manifest_facts() {
  bun -e '
    const m = Bun.YAML.parse(await Bun.file(process.argv[1]).text());
    const out = [];
    const rt = m.runtime || {};
    out.push("version\t" + (m.version || ""));
    out.push("ref\t" + ((rt.source || {}).ref || ""));
    out.push("artifacts\t" + Object.keys(rt.artifacts || {}).length);
    if (m.db && m.db.migrations) out.push("dir\t" + m.db.migrations.replace(/^\.?\//, "").replace(/\/$/, ""));
    const p = m.provides || {};
    for (const s of p.skills || []) if (s.body_file) out.push("file\t" + s.body_file.replace(/^\.?\//, ""));
    const entries = [m.icon];
    for (const k of ["ui_panels", "ui_components", "ui_surfaces"]) for (const e of p[k] || []) entries.push(e.entry);
    for (const e of entries) if (typeof e === "string" && e.startsWith("/ui/")) out.push("file\t" + e.slice(1));
    console.log(out.join("\n"));
  ' "$1"
}

FACTS="$(manifest_facts "$MANIFEST")"
fact() { printf '%s\n' "$FACTS" | awk -F'\t' -v k="$1" '$1==k{print $2}'; }
[ "$(fact version)" = "$VERSION" ] || die "apteva.yaml version is $(fact version), not $VERSION"
[ "$(fact ref)" = "$TAG" ] || die "runtime.source.ref is $(fact ref), not $TAG"
[ "$(fact artifacts)" = "0" ] || die "apteva.yaml already has runtime.artifacts; package the commit before that block is added"

cd "$ROOT"
[ -z "$(git status --porcelain -- "$APP_REL")" ] || die "uncommitted changes under $APP_REL; commit them first"

# The packaged UI must be the committed build of the committed TSX.
if ls "$APP_DIR"/ui/*.tsx >/dev/null 2>&1; then
  echo "==> rebuilding $APP panels to check the committed bundles"
  bun run scripts/build-panels.ts --app "$APP" >/dev/null
  if [ -n "$(git status --porcelain -- "$APP_REL")" ]; then
    git status --short -- "$APP_REL" >&2
    die "the committed UI bundles are stale; commit the rebuilt files and run again"
  fi
fi

WORK="$(mktemp -d "${TMPDIR:-/tmp}/package-$APP.XXXXXX")"
trap 'rm -rf "$WORK"' EXIT
echo "==> exporting $APP_REL from $(git rev-parse --short HEAD)"
mkdir -p "$WORK/tree"
git archive HEAD "$APP_REL" | tar -x -C "$WORK/tree"
TREE="$WORK/tree/$APP_REL"

# Everything the server requires inside the package, relative to src/<entry>.
REQUIRED=("apteva.yaml")
while IFS= read -r f; do [ -n "$f" ] && REQUIRED+=("$f"); done < <(fact file)
MIGRATIONS="$(fact dir)"
for f in "${REQUIRED[@]}"; do [ -f "$TREE/$f" ] || die "$APP_REL/$f is named by the manifest but not committed"; done
[ -z "$MIGRATIONS" ] || [ -d "$TREE/$MIGRATIONS" ] || die "$APP_REL/$MIGRATIONS is not committed"

rm -rf "$OUT"; mkdir -p "$OUT"
: > "$OUT/SHA256SUMS"
BLOCK="  artifacts:"
for platform in $PLATFORMS; do
  os="${platform%-*}"; arch="${platform#*-}"
  name="$APP-v$VERSION-$platform.tar.gz"
  stage="$WORK/stage-$platform"
  echo "==> $platform"
  mkdir -p "$stage/src/$APP_REL"
  (cd "$TREE" && GOWORK=off CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" \
    go build -trimpath -buildvcs=false -ldflags="-s -w" -o "$stage/bin" .)
  for f in "${REQUIRED[@]}"; do
    mkdir -p "$stage/src/$APP_REL/$(dirname "$f")"
    cp "$TREE/$f" "$stage/src/$APP_REL/$f"
  done
  if [ -n "$MIGRATIONS" ]; then
    mkdir -p "$stage/src/$APP_REL/$MIGRATIONS"
    cp -R "$TREE/$MIGRATIONS/." "$stage/src/$APP_REL/$MIGRATIONS/"
  fi
  # bsdtar on macOS adds AppleDouble (._*) files unless told not to; agent-worlds
  # 0.7.0 shipped them and needed a fix release.
  if tar --version 2>/dev/null | grep -q bsdtar; then
    (cd "$stage" && COPYFILE_DISABLE=1 tar --no-mac-metadata --no-xattrs --uid 0 --gid 0 --uname root --gname root -czf "$OUT/$name" bin src)
  else
    (cd "$stage" && tar --owner=0 --group=0 --numeric-owner --sort=name -czf "$OUT/$name" bin src)
  fi
  # Check what actually landed in the archive.
  listing="$(tar -tzf "$OUT/$name")"
  if printf '%s\n' "$listing" | grep -qE '(^|/)\._|__MACOSX|\.DS_Store'; then die "$name contains macOS metadata"; fi
  for f in bin "${REQUIRED[@]/#/src/$APP_REL/}"; do
    printf '%s\n' "$listing" | grep -qx "$f" || die "$name is missing $f"
  done
  sum="$(shasum -a 256 "$OUT/$name" | awk '{print $1}')"
  printf '%s  %s\n' "$sum" "$name" >> "$OUT/SHA256SUMS"
  BLOCK+=$'\n'"    $platform:"$'\n'"      url: $URL_BASE/$name"$'\n'"      sha256: $sum"
done
printf '%s\n' "$BLOCK" > "$OUT/artifacts.yaml"

if [ "$WRITE_MANIFEST" = 1 ]; then
  # Insert the block at the end of the top-level runtime: section, leaving the
  # rest of the file byte-for-byte as committed, then prove the result parses
  # to the same manifest plus artifacts.
  bun -e '
    const [path, blockPath] = process.argv.slice(1);
    const text = await Bun.file(path).text();
    const block = (await Bun.file(blockPath).text()).replace(/\n+$/, "");
    const lines = text.split("\n");
    const start = lines.findIndex(l => l === "runtime:");
    if (start < 0) throw new Error("no top-level runtime: section");
    let end = start + 1;
    while (end < lines.length && (lines[end] === "" || /^\s/.test(lines[end]) || lines[end].startsWith("#"))) end++;
    while (end > start + 1 && lines[end - 1].trim() === "") end--;
    const next = [...lines.slice(0, end), ...block.split("\n"), ...lines.slice(end)].join("\n");
    const before = Bun.YAML.parse(text), after = Bun.YAML.parse(next);
    const arts = after.runtime.artifacts;
    delete after.runtime.artifacts;
    if (JSON.stringify(before) !== JSON.stringify(after)) throw new Error("inserting artifacts changed other fields");
    if (Object.keys(arts).length !== 4) throw new Error("artifacts block did not parse");
    await Bun.write(path, next);
  ' "$MANIFEST" "$OUT/artifacts.yaml"
  echo "==> added runtime.artifacts to $APP_REL/apteva.yaml"
fi

echo
echo "Packages in $OUT:"
cat "$OUT/SHA256SUMS"
echo
if [ "$WRITE_MANIFEST" = 1 ]; then
  echo "Next: commit $APP_REL/apteva.yaml, tag that commit $TAG, push the tag, then"
else
  echo "Next: add $OUT/artifacts.yaml under runtime: (or rerun with --write-manifest), commit, tag $TAG, push the tag, then"
fi
echo "  gh release create $TAG --verify-tag --title \"$APP v$VERSION\" $OUT/*.tar.gz $OUT/SHA256SUMS"
echo "and bump the registry only after the release assets are up."
