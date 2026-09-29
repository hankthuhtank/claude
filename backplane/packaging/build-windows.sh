#!/usr/bin/env bash
# Builds the Windows release of Backplane (works on Linux and macOS):
#
#   dist/Backplane-<version>-Windows/       the folder users unzip
#     Install Backplane.exe                 per-user installer (NSIS)
#     Uninstall Backplane.exe               finds and runs the installed uninstaller
#     READ ME FIRST.txt
#     Guide/Backplane Guide.html            offline guide, fonts embedded
#     Legal/LICENSE.txt, THIRD-PARTY-NOTICES.txt
#     SHA256SUMS.txt
#   dist/Backplane-<version>-Windows.zip
#
# Needs Go, Node.js + npm (UI bundle), go-winres and NSIS 3 (makensis):
#   go install github.com/tc-hib/go-winres@latest
#   sudo apt install nsis        (or: brew install makensis)
#
# Usage: packaging/build-windows.sh [version]      default 1.1.0
#        SKIP_TESTS=1 packaging/build-windows.sh   skip vet and tests
set -euo pipefail

VERSION="${1:-1.1.0}"
[[ "$VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo "version must look like 1.2.3" >&2; exit 1; }

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PKG="$ROOT/packaging"
OUT="$ROOT/dist"
STAGE="$OUT/stage"
NAME="Backplane-$VERSION-Windows"
REL="$OUT/$NAME"
WINRES="$(command -v go-winres || echo "$(go env GOPATH)/bin/go-winres")"

say() { printf '\n== %s\n' "$*"; }
need() { command -v "$1" >/dev/null 2>&1 || [ -x "$1" ] || { echo "Missing tool: $1" >&2; exit 1; }; }
need go; need npm; need node; need makensis; need zip; need "$WINRES"
if command -v sha256sum >/dev/null 2>&1; then SHA="sha256sum"; else SHA="shasum -a 256"; fi
# Windows users open these in Notepad: write CRLF line endings.
crlf() { awk '{ sub(/\r$/, ""); printf "%s\r\n", $0 }'; }

rm -rf "$STAGE" "$REL" "$OUT/$NAME.zip"
mkdir -p "$STAGE" "$REL/Guide" "$REL/Legal"
cd "$ROOT"

say "UI bundle"
( cd ui && { [ -d node_modules ] || npm ci --no-audit --no-fund; } && npm run build --silent )

if [ "${SKIP_TESTS:-0}" != "1" ]; then
  say "Vet and tests"
  go vet ./...
  GOOS=windows go vet ./...
  go test ./...
fi

say "Windows resources (icon, manifest, version)"
"$WINRES" make --in "$PKG/winres/winres.json" --arch amd64 --out "$ROOT/cmd/backplane/rsrc" \
  --product-version "$VERSION.0" --file-version "$VERSION.0"

say "Backplane.exe"
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath \
  -ldflags "-H windowsgui -s -w -X main.version=$VERSION" -o "$STAGE/Backplane.exe" ./cmd/backplane

say "Icon and installer artwork"
go run ./packaging/imgtool ico -o "$STAGE/Backplane.ico" \
  packaging/icon/icon-{16,20,24,32,40,48,64,128,256}.png
go run ./packaging/imgtool bmp -o "$STAGE/welcome.bmp" packaging/installer/welcome.png
go run ./packaging/imgtool bmp -o "$STAGE/header.bmp" packaging/installer/header.png
cp packaging/icon/icon-256.png "$STAGE/Backplane.png"

say "Installer and uninstaller"
makensis -V2 -DVERSION="$VERSION" -DSTAGE="$STAGE" -DOUTFILE="$REL/Install Backplane.exe" "$PKG/installer/installer.nsi"
makensis -V2 -DVERSION="$VERSION" -DSTAGE="$STAGE" -DOUTFILE="$REL/Uninstall Backplane.exe" "$PKG/installer/uninstall.nsi"

say "Guide"
FONTS="$ROOT/internal/uiassets/dist/fonts"
face() { # family weight extra-css file
  printf '@font-face{font-family:"%s";font-weight:%s;%sfont-display:swap;src:url(data:font/woff2;base64,%s) format("woff2")}\n' \
    "$1" "$2" "$3" "$(base64 < "$FONTS/$4" | tr -d '\n')"
}
{
  face "Barlow Condensed" 600 "" barlow-condensed-latin-600-normal.woff2
  face "Barlow Condensed" 700 "" barlow-condensed-latin-700-normal.woff2
  face "Atkinson Next" 400 "" atkinson-hyperlegible-next-latin-400-normal.woff2
  face "Atkinson Next" 400 "font-style:italic;" atkinson-hyperlegible-next-latin-400-italic.woff2
  face "Atkinson Next" 700 "" atkinson-hyperlegible-next-latin-700-normal.woff2
  face "Atkinson Mono" 400 "" atkinson-hyperlegible-mono-latin-400-normal.woff2
  face "Atkinson Mono" 600 "" atkinson-hyperlegible-mono-latin-600-normal.woff2
} > "$STAGE/fonts.css"
awk -v f="$STAGE/fonts.css" '/\/\*@FONTS@\*\//{ while ((getline l < f) > 0) print l; next } { print }' "$PKG/release/guide.html" \
  | sed "s/{{VERSION}}/$VERSION/g" > "$REL/Guide/Backplane Guide.html"

say "Read-me, license and third-party notices"
sed "s/{{VERSION}}/$VERSION/g" "$PKG/release/READ ME FIRST.txt" | crlf > "$REL/READ ME FIRST.txt"
sed "s/{{VERSION}}/$VERSION/g" "$PKG/release/LICENSE.txt" | crlf > "$REL/Legal/LICENSE.txt"

rule() { printf '%s\n' "========================================================================"; }
section() { printf '\n'; rule; printf '%s\n' "$1"; rule; printf '\n'; }
{
  printf 'THIRD-PARTY NOTICES\n\nBackplane %s includes the open-source components listed below, each with its\nlicense. Thank you to their authors.\n' "$VERSION"

  section "Go standard library ($(go env GOVERSION))"
  cat "$(go env GOROOT)/LICENSE"

  # Exactly the modules compiled into Backplane.exe, with their license files.
  go list -m -f '{{.Path}} {{.Version}} {{.Dir}}' all > "$STAGE/modules.txt"
  while read -r path ver; do
    dir="$(awk -v p="$path" -v v="$ver" '$1 == p && $2 == v { $1 = ""; $2 = ""; sub(/^  /, ""); print; exit }' "$STAGE/modules.txt")"
    [ -n "$dir" ] || { echo "No module folder for $path@$ver (run go mod download)" >&2; exit 1; }
    lic="$(ls "$dir" | grep -iE '^(licen[cs]e|copying)(\.md|\.txt)?$' | head -1 || true)"
    [ -n "$lic" ] || { echo "No license file in $dir" >&2; exit 1; }
    section "$path $ver"
    cat "$dir/$lic"
    for n in NOTICE NOTICE.txt NOTICE.md; do
      [ -f "$dir/$n" ] && { printf '\n'; cat "$dir/$n"; }
    done
    if [ "$path" = "github.com/jchv/go-webview2" ]; then
      section "Microsoft Edge WebView2 Loader (WebView2Loader.dll, embedded by go-webview2)"
      cat "$dir/webviewloader/sdk/LICENSE.txt"
    fi
  done < <(go version -m "$STAGE/Backplane.exe" | awk '$1 == "dep" { print $2, $3 }')

  section "Preact $(node -p 'require("./ui/node_modules/preact/package.json").version')"
  cat ui/node_modules/preact/LICENSE

  for font in atkinson-hyperlegible-next atkinson-hyperlegible-mono barlow-condensed; do
    section "Font: $font (via @fontsource/$font $(node -p "require('./ui/node_modules/@fontsource/$font/package.json').version"))"
    cat "ui/node_modules/@fontsource/$font/LICENSE"
  done

  section "NSIS (Nullsoft Scriptable Install System), used to build the installer"
  printf '%s\n' "The installer and uninstaller are built with NSIS, licensed under the zlib/libpng" \
    "license. Its LZMA decompressor is licensed under the Common Public License with a" \
    "special exception for installers, and its bzip2 code under the bzip2 license." \
    "Details: https://nsis.sourceforge.io/License"
} | crlf > "$REL/Legal/THIRD-PARTY-NOTICES.txt"

say "Checksums and zip"
( cd "$REL" && $SHA "Install Backplane.exe" "Uninstall Backplane.exe" | crlf > SHA256SUMS.txt )
( cd "$OUT" && zip -qr -9 -X "$NAME.zip" "$NAME" )

say "Done"
ls -l "$REL" "$OUT/$NAME.zip"
