#!/bin/sh
# Ensure /data is writable by the tokenhub user.
# Handles volumes created by older images that ran as a different user (e.g. ubuntu/root).
[ -d /data ] && chown -R tokenhub:tokenhub /data 2>/dev/null || true
exec setpriv --reuid=tokenhub --regid=tokenhub --init-groups /tokenhub "$@"
