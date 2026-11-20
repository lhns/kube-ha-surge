#!/usr/bin/env bash
# Check that /manager built for each platform has that platform's ELF machine.
# Image index and config claim the right arch even when the binary does not.
#
# Usage: check-binary-arch.sh <dir> <platform>...   e.g. out linux/amd64 linux/arm64
# Expects <dir>/linux_amd64/manager etc., the layout of buildx
# `--output type=local,dest=<dir>,platform-split=true`.
set -euo pipefail

dir=${1:?usage: $0 <dir> <platform>...}
shift
[ $# -gt 0 ] || { echo "no platforms given" >&2; exit 2; }

fail=0
for platform in "$@"; do
  case "$platform" in
    linux/amd64) want=3e00; name=x86-64 ;;
    linux/arm64) want=b700; name=aarch64 ;;
    *) echo "unsupported platform: $platform" >&2; exit 2 ;;
  esac
  bin="$dir/${platform//\//_}/manager"
  if [ ! -f "$bin" ]; then
    echo "FAIL $platform: $bin not found" >&2; fail=1; continue
  fi
  # ELF e_machine: 2 bytes at offset 18, little-endian on both targets.
  found=$(od -An -tx1 -j18 -N2 "$bin" | tr -d ' \n')
  if [ "$found" = "$want" ]; then
    echo "ok   $platform: $name"
  else
    echo "FAIL $platform: want e_machine $want ($name), found $found in $bin" >&2; fail=1
  fi
done
exit $fail
