#!/bin/sh
# Render frps.toml from /etc/frp/frps.toml.tmpl using the environment, then
# exec frps. envsubst (gettext) only expands the variables we name, so other
# `$`-bearing values in the template are left alone.
set -eu

: "${HATCHWAY_PLUGIN_SECRET:?HATCHWAY_PLUGIN_SECRET is required}"
: "${HATCHWAY_TUNNEL_DOMAIN:?HATCHWAY_TUNNEL_DOMAIN is required}"
: "${HATCHWAY_FRPS_AUTH_TOKEN:?HATCHWAY_FRPS_AUTH_TOKEN is required}"

OUT=/var/run/frps/frps.toml
envsubst '${HATCHWAY_PLUGIN_SECRET} ${HATCHWAY_TUNNEL_DOMAIN} ${HATCHWAY_FRPS_AUTH_TOKEN}' \
	</etc/frp/frps.toml.tmpl >"${OUT}"

exec frps -c "${OUT}"
