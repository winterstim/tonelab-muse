#!/bin/sh
# Installs the Muse extension into the Ableton Live OSC remote script.
# Idempotent: copies muse.py and registers its handler once.
set -e
here=$(cd "$(dirname "$0")/.." && pwd)
dst="${ABLETON_OSC_DIR:-$HOME/Music/Ableton/User Library/Remote Scripts/AbletonOSC}"
[ -d "$dst/abletonosc" ] || { echo "OSC remote script not found at $dst"; exit 1; }
cp "$here/deploy/ableton/muse.py" "$dst/abletonosc/muse.py"
grep -q "attach_to_running_manager" "$dst/abletonosc/__init__.py" || printf "import importlib as _il\nfrom . import muse as _muse\n_il.reload(_muse)\nfrom .muse import MuseHandler, attach_to_running_manager\nattach_to_running_manager()\n" >> "$dst/abletonosc/__init__.py"
# manager.py also lists it, for a fresh start of Live.
grep -q "MuseHandler" "$dst/manager.py" || sed -i.bak 's/^\( *\)abletonosc.MidiMapHandler(self),/&\
\1abletonosc.MuseHandler(self),/' "$dst/manager.py"
grep -q "MuseHandler" "$dst/manager.py" && echo "installed; reload the script or restart Live"
