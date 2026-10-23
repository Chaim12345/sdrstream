#!/usr/bin/env bash
# One-command install for sdrstream on Linux and macOS.
#
#   curl -fsSL https://raw.githubusercontent.com/Chaim12345/sdrstream/master/scripts/install.sh | bash
#   -- or, from a checkout:  scripts/install.sh
#
# What it does:
#   1. installs the sdrstream binary (GitHub release when available,
#      otherwise builds from this source tree with a Go toolchain it
#      fetches privately into ~/.toolchain);
#   2. creates a state directory and a first-run password WITHOUT ever
#      printing it (interactive prompt, or random + stored mode 600);
#   3. registers a service: systemd (Linux) or launchd (macOS);
#   4. optionally installs reference decoders jt9/wsprd rootlessly via
#      scripts/refdec.sh (separate processes; their GPL terms do not
#      extend to sdrstream and vice versa).
#
# Env knobs: SDRSTREAM_VERSION (default: latest), SDRSTREAM_PREFIX
# (default: /usr/local on Linux, ~/.local on macOS), SDRSTREAM_PORT
# (default 8000), SDRSTREAM_NO_SERVICE=1 to skip service registration,
# SDRSTREAM_WITH_REFDEC=1 to install jt9/wsprd.
set -euo pipefail

REPO="Chaim12345/sdrstream"
VERSION="${SDRSTREAM_VERSION:-latest}"
PORT="${SDRSTREAM_PORT:-8000}"
OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"
case "$ARCH" in
  x86_64|amd64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) echo "unsupported architecture: $ARCH" >&2; exit 1 ;;
esac
case "$OS" in
  linux|darwin) ;;
  *) echo "unsupported OS: $OS (on Windows use scripts/install.ps1)" >&2; exit 1 ;;
esac

# Install scope: system-wide when root (or passwordless sudo), otherwise
# rootless under $HOME - no sudo prompt, everything user-owned.
SCOPE=user
SUDO=""
if [ "$OS" = darwin ]; then
  :
elif [ "$(id -u)" = 0 ]; then
  SCOPE=system
elif sudo -n true 2>/dev/null; then
  SCOPE=system
  SUDO="sudo"
fi
if [ "$SCOPE" = system ]; then
  PREFIX="${SDRSTREAM_PREFIX:-/usr/local}"
  STATE_DIR="${SDRSTREAM_STATE_DIR:-/var/lib/sdrstream}"
  CONF_DIR="/etc/sdrstream"
else
  PREFIX="${SDRSTREAM_PREFIX:-$HOME/.local}"
  if [ "$OS" = darwin ]; then
    STATE_DIR="${SDRSTREAM_STATE_DIR:-$HOME/Library/Application Support/sdrstream}"
  else
    STATE_DIR="${SDRSTREAM_STATE_DIR:-$HOME/.local/share/sdrstream}"
  fi
  CONF_DIR="$HOME/.config/sdrstream"
fi
NEED_SUDO=0
[ "$SCOPE" = system ] && NEED_SUDO=1

SRC_DIR="$(cd "$(dirname "$0")/.." 2>/dev/null && pwd || true)"

have_binary=0
install_release() {
  local url tmp
  if [ "$VERSION" = latest ]; then
    url="https://github.com/$REPO/releases/latest/download/sdrstream_${OS}_${ARCH}"
  else
    url="https://github.com/$REPO/releases/download/$VERSION/sdrstream_${VERSION}_${OS}_${ARCH}"
  fi
  tmp="$(mktemp)"
  if curl -fsSL "$url" -o "$tmp" 2>/dev/null; then
    chmod +x "$tmp"
    $SUDO install -D -m 0755 "$tmp" "$PREFIX/bin/sdrstream"
    rm -f "$tmp"
    have_binary=1
  else
    rm -f "$tmp"
  fi
}

build_from_source() {
  [ -n "$SRC_DIR" ] && [ -f "$SRC_DIR/go/go.mod" ] || { echo "no release available and not running from a source checkout" >&2; exit 1; }
  local gobin
  if command -v go >/dev/null 2>&1; then
    gobin="$(command -v go)"
  else
    local tc="$HOME/toolchain"
    if [ ! -x "$tc/go/bin/go" ]; then
      echo "fetching a private Go toolchain into $tc (not system-wide)"
      local gov="1.25.0" tarball
      mkdir -p "$tc"
      tarball="$(mktemp)"
      curl -fsSL "https://go.dev/dl/go${gov}.${OS}-${ARCH}.tar.gz" -o "$tarball"
      tar -C "$tc" -xzf "$tarball"
      rm -f "$tarball"
    fi
    gobin="$tc/go/bin/go"
  fi
  ( cd "$SRC_DIR/go" && CGO_ENABLED=0 "$gobin" build -trimpath -ldflags "-s -w" -o /tmp/sdrstream-install ./cmd/sdrstream )
  $SUDO install -D -m 0755 /tmp/sdrstream-install "$PREFIX/bin/sdrstream"
  rm -f /tmp/sdrstream-install
  have_binary=1
}

echo "==> installing sdrstream ($OS/$ARCH) into $PREFIX"
install_release || true
[ "$have_binary" = 1 ] || build_from_source

# --- first-run password: set without ever printing it -------------------
$SUDO mkdir -p "$CONF_DIR" "$STATE_DIR"
ENV_FILE="$CONF_DIR/sdrstream.env"
if [ ! -f "$ENV_FILE" ]; then
  umask 077
  if [ -t 0 ]; then
    # Interactive: ask once, never echo.
    while :; do
      IFS= read -rs -p "Choose a password for the sdrstream web UI: " pw1; echo
      [ -n "$pw1" ] || { echo "password cannot be empty"; continue; }
      IFS= read -rs -p "Repeat it: " pw2; echo
      [ "$pw1" = "$pw2" ] && break || echo "passwords did not match"
    done
  else
    # Non-interactive: generate one and store it; the file IS the record.
    pw1="$(head -c 24 /dev/urandom | od -An -tx1 | tr -d ' \n')"
    echo "generated a random UI password (not shown; see $ENV_FILE)"
  fi
  $SUDO tee "$ENV_FILE" >/dev/null <<ENVEOF
SDRSTREAM_UI_PASSWORD=$pw1
SDRSTREAM_STATE_DIR=$STATE_DIR
ENVEOF
  unset pw1 pw2 2>/dev/null || true
  $SUDO chmod 600 "$ENV_FILE"
else
  echo "keeping existing $ENV_FILE"
fi

# --- optional reference decoders (jt9/wsprd) -----------------------------
if [ "${SDRSTREAM_WITH_REFDEC:-0}" = 1 ] && [ -f "$SRC_DIR/scripts/refdec.sh" ]; then
  echo "==> installing reference decoders rootlessly (jt9/wsprd)"
  ( . "$SRC_DIR/scripts/refdec.sh" ) || echo "refdec install failed; Go decoders will be used"
  if [ -n "${SDRSTREAM_REFDEC_DIR:-}" ]; then
    echo "SDRSTREAM_REFDEC_DIR=$SDRSTREAM_REFDEC_DIR" | $SUDO tee -a "$ENV_FILE" >/dev/null
  fi
fi

# --- service registration -------------------------------------------------
if [ "${SDRSTREAM_NO_SERVICE:-0}" != 1 ]; then
  if [ "$OS" = linux ] && command -v systemctl >/dev/null 2>&1; then
    if [ "$SCOPE" = system ]; then
      if ! id -u sdrstream >/dev/null 2>&1; then
        $SUDO useradd --system --home-dir "$STATE_DIR" --shell /usr/sbin/nologin sdrstream || true
      fi
      $SUDO chown -R sdrstream:sdrstream "$STATE_DIR" || true
      if [ -n "$SRC_DIR" ] && [ -f "$SRC_DIR/deploy/sdrstream.service" ]; then
        $SUDO install -m 0644 "$SRC_DIR/deploy/sdrstream.service" /etc/systemd/system/sdrstream.service
        $SUDO systemctl daemon-reload
        $SUDO systemctl enable --now sdrstream
        echo "systemd service enabled: systemctl status sdrstream"
      else
        echo "deploy/sdrstream.service not found; skipping unit install"
      fi
    else
      UNIT_DIR="$HOME/.config/systemd/user"
      if [ -n "$SRC_DIR" ] && [ -f "$SRC_DIR/deploy/sdrstream-user.service" ]; then
        mkdir -p "$UNIT_DIR"
        install -m 0644 "$SRC_DIR/deploy/sdrstream-user.service" "$UNIT_DIR/sdrstream.service"
        systemctl --user daemon-reload || true
        systemctl --user enable --now sdrstream           && echo "user service enabled: systemctl --user status sdrstream"           || echo "could not start a user service (no lingering?); run: $PREFIX/bin/sdrstream web"
        echo "tip: loginctl enable-linger $USER keeps it running after logout"
      fi
    fi
  elif [ "$OS" = darwin ]; then
    PLIST_DIR="$HOME/Library/LaunchAgents"
    mkdir -p "$PLIST_DIR" "$HOME/Library/Logs/sdrstream"
    cat > "$PLIST_DIR/com.chaim12345.sdrstream.plist" <<PLISTEOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>com.chaim12345.sdrstream</string>
  <key>ProgramArguments</key><array>
    <string>$PREFIX/bin/sdrstream</string><string>web</string>
    <string>--host</string><string>127.0.0.1</string>
    <string>--port</string><string>$PORT</string>
  </array>
  <key>EnvironmentVariables</key><dict>
    <key>SDRSTREAM_UI_PASSWORD</key><string>$(grep '^SDRSTREAM_UI_PASSWORD=' "$ENV_FILE" | cut -d= -f2-)</string>
    <key>SDRSTREAM_STATE_DIR</key><string>$STATE_DIR</string>
  </dict>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardOutPath</key><string>$HOME/Library/Logs/sdrstream/out.log</string>
  <key>StandardErrorPath</key><string>$HOME/Library/Logs/sdrstream/err.log</string>
</dict></plist>
PLISTEOF
    chmod 600 "$PLIST_DIR/com.chaim12345.sdrstream.plist"
    launchctl unload "$PLIST_DIR/com.chaim12345.sdrstream.plist" 2>/dev/null || true
    launchctl load "$PLIST_DIR/com.chaim12345.sdrstream.plist"
    echo "launchd agent loaded: launchctl list | grep sdrstream"
  fi
fi

echo
echo "==> done. Open http://127.0.0.1:$PORT and log in."
echo "    The UI password is stored (never printed) in $ENV_FILE"
[ "$SCOPE" = system ] && echo "    read it once with: sudo grep UI_PASSWORD $ENV_FILE"
echo "    To expose beyond localhost, re-run with the service host set to"
echo "    0.0.0.0 and put the UI behind TLS."
