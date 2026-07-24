#!/bin/sh
# Render frps.toml from /etc/frp/frps.toml.tmpl using the environment, then
# exec frps. envsubst (gettext) only expands the variables we name, so other
# `$`-bearing values in the template are left alone.
set -eu
umask 077

: "${HATCHWAY_PLUGIN_SECRET:?HATCHWAY_PLUGIN_SECRET is required}"
: "${HATCHWAY_TUNNEL_DOMAIN:?HATCHWAY_TUNNEL_DOMAIN is required}"
: "${HATCHWAY_FRPS_AUTH_TOKEN:?HATCHWAY_FRPS_AUTH_TOKEN is required}"

FRPS_CONFIG_PATH=/var/run/frps/frps.toml
FRPS_CONFIG_TMP="${FRPS_CONFIG_PATH}.tmp"
trap 'rm -f "${FRPS_CONFIG_TMP}"' EXIT HUP INT TERM

envsubst '${HATCHWAY_PLUGIN_SECRET} ${HATCHWAY_TUNNEL_DOMAIN} ${HATCHWAY_FRPS_AUTH_TOKEN}' \
	</etc/frp/frps.toml.tmpl >"${FRPS_CONFIG_TMP}"
chmod 0600 "${FRPS_CONFIG_TMP}"
frps verify -c "${FRPS_CONFIG_TMP}" >/dev/null
mv "${FRPS_CONFIG_TMP}" "${FRPS_CONFIG_PATH}"

exec frps -c "${FRPS_CONFIG_PATH}"
