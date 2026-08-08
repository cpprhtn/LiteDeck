#!/bin/sh
# Start sshd, then hand off to the dind entrypoint as PID 1 so the daemon still
# receives signals normally.
/usr/sbin/sshd -e
exec dockerd-entrypoint.sh "$@"
