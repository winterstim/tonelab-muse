#!/bin/sh
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
out=${RELEASE_OUT_DIR:-"$root/dist/release"}
work=$(mktemp -d "${TMPDIR:-/tmp}/muse-release.XXXXXX")
trap 'rm -rf "$work"' EXIT HUP INT TERM
cd "$root"

mkdir -p "$out"
find "$out" -maxdepth 1 -type f -name 'tonelab-muse-*.zip' -delete

package() {
	platform=$1
	binary=$2
	launcher=$3
	stage="$work/$platform"
	mkdir -p "$stage/deploy/ableton" "$stage/deploy/reaper/bridge" \
		"$stage/scripts"
	if [ "$platform" = windows-amd64 ]; then
		cp "$binary" "$stage/tonelab-muse-windows-amd64.exe"
	else
		cp "$binary" "$stage/tonelab-muse"
	fi
	cp "$root/deploy/ableton/muse.py" "$stage/deploy/ableton/muse.py"
	cp "$root/internal/daw/reaper/bridge/startup.lua" "$stage/deploy/reaper/bridge/startup.lua"
	cp "$root/internal/daw/oscmap/bundled/abletonosc.patch" "$stage/deploy/ableton/abletonosc.patch"
	cp "$root/scripts/install-ableton-ext.sh" "$stage/scripts/install-ableton-ext.sh"
	cp "$root/$launcher" "$stage/$(basename "$launcher")"
	if [ "$platform" = windows-amd64 ]; then
		cat > "$stage/INSTALL.md" <<'EOF'
1. Unzip this archive and keep its folders together.
2. For the Ableton Remote Script, install AbletonOSC per `deploy/ableton/abletonosc.patch`, then run `scripts/install-ableton-ext.sh`.
3. Open your DAW; the REAPER bridge is installed automatically when the app connects.
4. Set `MODEL_API_KEY`, or let `start.bat` prompt for it.
5. Run `start.bat` and open `http://localhost:7400` if it does not open automatically.
EOF
	else
		cat > "$stage/INSTALL.md" <<'EOF'
1. Unzip this archive and keep its folders together.
2. For the Ableton Remote Script, install AbletonOSC per `deploy/ableton/abletonosc.patch`, then run `scripts/install-ableton-ext.sh`.
3. Open your DAW; the REAPER bridge is installed automatically when the app connects.
4. Set `MODEL_API_KEY`, or let `start.command` prompt for it.
5. Run `./start.command` and open `http://localhost:7400` if it does not open automatically.
EOF
		chmod +x "$stage/$(basename "$launcher")" "$stage/tonelab-muse"
	fi
	(cd "$stage" && zip -qr "$out/tonelab-muse-$platform.zip" .)
}

mkdir -p "$work/bin"
CGO_ENABLED=1 GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags='-s -w' \
	-o "$work/bin/tonelab-muse-darwin-arm64" "$root/cmd/tonelab-muse"
CGO_ENABLED=1 GOOS=darwin GOARCH=amd64 go build -trimpath -ldflags='-s -w' \
	-o "$work/bin/tonelab-muse-darwin-amd64" "$root/cmd/tonelab-muse"
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags='-s -w' \
	-o "$work/bin/tonelab-muse-windows-amd64.exe" "$root/cmd/tonelab-muse"

package darwin-arm64 "$work/bin/tonelab-muse-darwin-arm64" scripts/start.command
package darwin-amd64 "$work/bin/tonelab-muse-darwin-amd64" scripts/start.command
package windows-amd64 "$work/bin/tonelab-muse-windows-amd64.exe" scripts/start.bat

printf 'Release archives written to %s\n' "$out"
ls -lh "$out"/tonelab-muse-*.zip
