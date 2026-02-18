#!/usr/bin/env bash
#
# setup-docker.sh — align Docker CLI plugins to the best available configuration.
#
# Problems this script fixes:
#
#   1. Stale Docker Desktop symlinks — Docker Desktop creates symlinks under
#      ~/.docker/cli-plugins/ pointing to /Applications/Docker.app/…
#      When Docker is later installed via Homebrew (or Docker Desktop is removed),
#      those symlinks break and shadow the working Homebrew binaries.
#
#   2. Non-executable plugin binaries — manually downloaded binaries (e.g.
#      buildx-v0.30.1.darwin-arm64) may lack the executable permission bit.
#
#   3. Stale docker contexts — old contexts from colima, orbstack, or Docker
#      Desktop that point to non-existent sockets clutter `docker context ls`
#      and create phantom buildx builders.
#
# Safe to run repeatedly (idempotent).  Prints nothing when no repairs are needed.

set -euo pipefail

PLUGINS_DIR="${HOME}/.docker/cli-plugins"

# Quick check: if docker buildx already works and there are no broken symlinks,
# nothing to fix.
if docker buildx version >/dev/null 2>&1; then
    has_broken=false
    if [ -d "$PLUGINS_DIR" ]; then
        for link in "$PLUGINS_DIR"/docker-*; do
            if [ -L "$link" ] && [ ! -e "$link" ]; then
                has_broken=true
                break
            fi
        done
    fi
    if ! $has_broken; then
        exit 0
    fi
fi

# ── 1. Fix broken CLI plugin symlinks ──

if [ -d "$PLUGINS_DIR" ]; then
    for link in "$PLUGINS_DIR"/docker-*; do
        [ -L "$link" ] || continue       # skip non-symlinks
        [ -e "$link" ] && continue       # skip working symlinks

        plugin_name=$(basename "$link")   # e.g. docker-buildx
        brew_formula="docker-${plugin_name#docker-}"
        brew_prefix=$(brew --prefix "$brew_formula" 2>/dev/null) || true
        brew_bin="${brew_prefix}/bin/${plugin_name}"

        if [ -n "$brew_prefix" ] && [ -x "$brew_bin" ]; then
            rm -f "$link"
            ln -s "$brew_bin" "$link"
            echo "setup: fixed $plugin_name -> $brew_bin"
        else
            rm -f "$link"
            echo "setup: removed broken $plugin_name (no replacement found)"
        fi
    done

    # Fix permissions on standalone plugin binaries.
    for bin in "$PLUGINS_DIR"/*; do
        [ -f "$bin" ] || continue
        [ -L "$bin" ] && continue
        [ -x "$bin" ] && continue
        chmod +x "$bin"
        echo "setup: fixed permissions on $(basename "$bin")"
    done
fi

# ── 2. Remove stale docker contexts ──

for ctx in $(docker context ls --format '{{.Name}}' 2>/dev/null); do
    [ "$ctx" = "default" ] && continue
    endpoint=$(docker context inspect "$ctx" --format '{{.Endpoints.docker.Host}}' 2>/dev/null) || continue
    socket="${endpoint#unix://}"
    if [ ! -S "$socket" ]; then
        docker context rm "$ctx" >/dev/null 2>&1 && \
            echo "setup: removed stale docker context '$ctx' ($socket not found)"
    fi
done

# ── 3. Verify ──

if docker buildx version >/dev/null 2>&1; then
    echo "setup: docker buildx $(docker buildx version 2>/dev/null | awk '{print $2}')"
fi
