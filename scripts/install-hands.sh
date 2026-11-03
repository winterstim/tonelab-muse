#!/bin/sh
# Builds the Muse Hands helper app into ~/Applications. macOS ties its
# permissions to the signature: signed with a stable certificate (a free
# Apple Development one from Xcode) they survive rebuilds; ad-hoc signed,
# every build has to be allowed again.
set -e
cd "$(dirname "$0")/.."
app="$HOME/Applications/Muse Hands.app"
pkill -x hands 2>/dev/null || true
rm -rf "$app"
mkdir -p "$app/Contents/MacOS"
go build -o "$app/Contents/MacOS/hands" ./cmd/hands
cat > "$app/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>CFBundleIdentifier</key><string>app.muse.hands</string>
  <key>CFBundleName</key><string>Muse Hands</string>
  <key>CFBundleExecutable</key><string>hands</string>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>CFBundleShortVersionString</key><string>1.0</string>
  <key>LSUIElement</key><true/>
</dict></plist>
PLIST
id=$(security find-identity -v -p codesigning | awk -F'"' '/Apple Development|Developer ID Application/ {print $2; exit}')
if [ -n "$id" ]; then
  codesign --force --sign "$id" --identifier app.muse.hands "$app"
  echo "signed with $id: permissions will survive rebuilds"
else
  codesign --force --sign - --identifier app.muse.hands "$app"
  echo "ad-hoc signed: allow it again after every rebuild (see the script header)"
fi
echo "installed $app"
