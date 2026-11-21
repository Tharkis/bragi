#!/bin/sh
# Runs Bragi as PUID:PGID so files it writes are owned by the right user
# (on Unraid: nobody:users, 99:100).
set -e

umask "${UMASK:-002}"

if [ "$(id -u)" = "0" ]; then
    PUID="${PUID:-99}"
    PGID="${PGID:-100}"
    mkdir -p "$BRAGI_CONFIG_DIR"
    # Fix ownership only when it is wrong, so big config folders start fast.
    if [ "$(stat -c '%u:%g' "$BRAGI_CONFIG_DIR")" != "$PUID:$PGID" ]; then
        chown -R "$PUID:$PGID" "$BRAGI_CONFIG_DIR"
    fi
    exec su-exec "$PUID:$PGID" /usr/local/bin/bragi "$@"
fi

# Already started as a non-root user (e.g. docker run --user).
exec /usr/local/bin/bragi "$@"
