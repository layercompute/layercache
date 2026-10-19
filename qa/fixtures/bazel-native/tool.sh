#!/bin/sh
set -eu
printf 'tool-v1:%s:' "${FIXTURE_ENV:?}" > "$2"
cat "$1" >> "$2"
