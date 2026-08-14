#!/usr/bin/env sh
# Install bwg from the latest GitHub release.
#
#   curl -fsSL https://raw.githubusercontent.com/lroolle/bwg-cli/main/install.sh | bash
#   ... | bash -s -- --system          # install to /usr/local/bin
#   ... | bash -s -- --bin-dir ~/bin
#   ... | bash -s -- --skill           # also install the Claude Code skill
#   BWG_VERSION=v0.2.0 ... | bash      # pin a version instead of latest
set -eu

REPO="lroolle/bwg-cli"
BIN_DIR="${BWG_BIN_DIR:-$HOME/.local/bin}"
SKILL_DIR="${BWG_SKILL_DIR:-$HOME/.claude/skills}"
WANT_SKILL=0

while [ $# -gt 0 ]; do
  case "$1" in
    --system)   BIN_DIR=/usr/local/bin ;;
    --bin-dir)  BIN_DIR="$2"; shift ;;
    --skill)    WANT_SKILL=1 ;;
    --skill-dir) SKILL_DIR="$2"; WANT_SKILL=1; shift ;;
    -h|--help)
      sed -n '2,9p' "$0" | sed 's/^# \{0,1\}//'
      exit 0 ;;
    *) echo "unknown option: $1" >&2; exit 2 ;;
  esac
  shift
done

die() { echo "error: $*" >&2; exit 1; }

case "$(uname -s)" in
  Darwin) OS=macOS ;;
  Linux)  OS=linux ;;
  *)      die "unsupported OS $(uname -s) — install with: go install github.com/$REPO/cmd/bwg@latest" ;;
esac
case "$(uname -m)" in
  x86_64|amd64)  ARCH=x86_64 ;;
  arm64|aarch64) ARCH=arm64 ;;
  *)             die "unsupported architecture $(uname -m)" ;;
esac

command -v curl >/dev/null 2>&1 || die "curl is required"
command -v tar  >/dev/null 2>&1 || die "tar is required"

# Resolve the latest tag from the redirect on /releases/latest rather
# than from api.github.com, which allows 60 unauthenticated requests an
# hour *per IP*. Behind a shared NAT, a VPN exit or a CI runner that
# budget is often already spent by someone else, and the install fails
# at its first step for a reason that has nothing to do with the user.
# The API stays as a fallback.
TAG="${BWG_VERSION:-}"
if [ -z "$TAG" ]; then
  TAG=$(curl -fsSLI -o /dev/null -w '%{url_effective}' \
        "https://github.com/$REPO/releases/latest" 2>/dev/null \
        | sed -n 's|.*/tag/||p')
fi
if [ -z "$TAG" ]; then
  TAG=$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" 2>/dev/null \
        | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -1)
fi
[ -n "$TAG" ] || die "could not determine the latest release — pick one yourself:
  BWG_VERSION=v0.2.0 ... | bash
  or download it from https://github.com/$REPO/releases/latest"

ASSET="bwg_${OS}_${ARCH}.tar.gz"
URL="https://github.com/$REPO/releases/download/$TAG/$ASSET"

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

echo "Downloading bwg $TAG for $OS/$ARCH..."
curl -fsSL "$URL" -o "$TMP/$ASSET" || die "download failed: $URL"

# Verify against the release checksums when they are published.
if curl -fsSL "https://github.com/$REPO/releases/download/$TAG/checksums.txt" -o "$TMP/checksums.txt" 2>/dev/null; then
  if command -v sha256sum >/dev/null 2>&1; then
    (cd "$TMP" && grep " $ASSET\$" checksums.txt | sha256sum -c -) || die "checksum mismatch"
  elif command -v shasum >/dev/null 2>&1; then
    (cd "$TMP" && grep " $ASSET\$" checksums.txt | shasum -a 256 -c -) || die "checksum mismatch"
  fi
fi

tar -xzf "$TMP/$ASSET" -C "$TMP"
mkdir -p "$BIN_DIR"
install -m 0755 "$TMP/bwg" "$BIN_DIR/bwg" 2>/dev/null \
  || { cp "$TMP/bwg" "$BIN_DIR/bwg" && chmod 0755 "$BIN_DIR/bwg"; }
echo "Installed $BIN_DIR/bwg"

if [ "$WANT_SKILL" = 1 ]; then
  mkdir -p "$SKILL_DIR/bwg-cli"
  if [ -f "$TMP/skills/bwg-cli/SKILL.md" ]; then
    cp "$TMP/skills/bwg-cli/SKILL.md" "$SKILL_DIR/bwg-cli/SKILL.md"
  else
    curl -fsSL "https://raw.githubusercontent.com/$REPO/main/skills/bwg-cli/SKILL.md" \
      -o "$SKILL_DIR/bwg-cli/SKILL.md"
  fi
  echo "Installed $SKILL_DIR/bwg-cli/SKILL.md"
fi

case ":$PATH:" in
  *":$BIN_DIR:"*) ;;
  *) echo; echo "Add $BIN_DIR to your PATH:"; echo "  export PATH=\"$BIN_DIR:\$PATH\"" ;;
esac

echo
echo "Next:"
echo "  export BWG_VEID=<id> BWG_API_KEY=<key>   # from KiwiVM > API"
echo "  bwg ls"
