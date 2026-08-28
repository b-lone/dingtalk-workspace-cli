#!/usr/bin/env bash
set -Eeuo pipefail

umask 077

readonly DEFAULT_SERVICE_ROOT="/Users/yuanzhan/Library/Application Support/DWSService"

fail() {
    printf 'ERROR: %s\n' "$1" >&2
    exit 1
}

usage() {
    printf '%s\n' \
        "Usage: dws-service-auth.sh --agent-code AGENT_CODE <auth arguments...>" \
        "Example: dws-service-auth.sh --agent-code dws-http-service login --device"
}

agent_code=""
while (($# > 0)); do
    case "$1" in
        --agent-code)
            (($# >= 2)) || fail "--agent-code requires a value"
            agent_code="$2"
            shift 2
            ;;
        --help|-h)
            usage
            exit 0
            ;;
        *)
            break
            ;;
    esac
done

[[ "$agent_code" =~ ^[A-Za-z0-9_-]{1,64}$ ]] || fail "--agent-code must match [A-Za-z0-9_-]{1,64}"
(($# > 0)) || fail "an auth command is required"

readonly service_root="${DWS_SERVICE_ROOT:-$DEFAULT_SERVICE_ROOT}"
readonly dws_binary="${DWS_SERVICE_CLI_BINARY:-${service_root}/current/dws}"
readonly config_dir="${service_root}/state/config"
readonly keychain_dir="${service_root}/state/keychain"

[[ "$service_root" = /* && "$dws_binary" = /* ]] || fail "service and CLI paths must be absolute"
[[ -f "$dws_binary" && -x "$dws_binary" ]] || fail "DWSService CLI is unavailable"

mkdir -p "$config_dir" "$keychain_dir"
chmod 0700 "$service_root" "${service_root}/state" "$config_dir" "$keychain_dir"

exec env \
    HOME="/Users/yuanzhan" \
    USER="yuanzhan" \
    LOGNAME="yuanzhan" \
    DWS_CONFIG_DIR="$config_dir" \
    DWS_KEYCHAIN_DIR="$keychain_dir" \
    DINGTALK_DWS_AGENTCODE="$agent_code" \
    "$dws_binary" auth "$@"
