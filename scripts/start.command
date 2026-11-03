#!/bin/bash
set -e

DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$DIR"

KEY_FILE="$DIR/.model_api_key"

if [ -z "$MODEL_API_KEY" ]; then
  if [ -f "$KEY_FILE" ]; then
    MODEL_API_KEY="$(cat "$KEY_FILE")"
  fi
fi

if [ -z "$MODEL_API_KEY" ]; then
  printf "Enter your MODEL_API_KEY: "
  read -r MODEL_API_KEY
  if [ -n "$MODEL_API_KEY" ]; then
    touch "$KEY_FILE"
    chmod 0600 "$KEY_FILE"
    printf "%s\n" "$MODEL_API_KEY" > "$KEY_FILE"
  else
    echo "MODEL_API_KEY cannot be empty."
    exit 1
  fi
fi

export MODEL_API_KEY

BIN="./tonelab-muse"
if [ ! -x "$BIN" ]; then
  ARCH="$(uname -m)"
  if [ "$ARCH" = "arm64" ] && [ -x "./tonelab-muse-darwin-arm64" ]; then
    BIN="./tonelab-muse-darwin-arm64"
  elif [ -x "./tonelab-muse-darwin-amd64" ]; then
    BIN="./tonelab-muse-darwin-amd64"
  fi
fi

if [ ! -x "$BIN" ]; then
  echo "Executable tonelab-muse binary not found in $DIR"
  exit 1
fi

"$BIN" serve &
SERVER_PID=$!
trap 'kill $SERVER_PID 2>/dev/null' EXIT INT TERM

sleep 1
open "http://localhost:7400"

wait "$SERVER_PID"
