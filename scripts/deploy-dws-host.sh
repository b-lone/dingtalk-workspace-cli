#!/usr/bin/env bash
set -Eeuo pipefail

umask 077

readonly LABEL="com.alibaba.dws-http"
readonly DEFAULT_SERVICE_ROOT="/Users/yuanzhan/Documents/Data/dws-service"
readonly DEFAULT_WRAPPER_PATH="/Users/yuanzhan/.qoderwork/bin/dws"
readonly DEFAULT_LAUNCH_AGENT="/Users/yuanzhan/Library/LaunchAgents/${LABEL}.plist"

fail() {
    printf 'ERROR: %s\n' "$1" >&2
    exit 1
}

usage() {
    printf '%s\n' \
        "Usage: deploy-dws-host.sh --binary PATH --git-sha SHA --profile CORP_ID" \
        "       --verify-group OPEN_CONVERSATION_ID --verify-title TITLE"
}

binary_path=""
git_sha=""
profile=""
verify_group=""
verify_title=""

while (($# > 0)); do
    case "$1" in
        --binary)
            (($# >= 2)) || fail "--binary requires a value"
            binary_path="$2"
            shift 2
            ;;
        --git-sha)
            (($# >= 2)) || fail "--git-sha requires a value"
            git_sha="$2"
            shift 2
            ;;
        --profile)
            (($# >= 2)) || fail "--profile requires a value"
            profile="$2"
            shift 2
            ;;
        --verify-group)
            (($# >= 2)) || fail "--verify-group requires a value"
            verify_group="$2"
            shift 2
            ;;
        --verify-title)
            (($# >= 2)) || fail "--verify-title requires a value"
            verify_title="$2"
            shift 2
            ;;
        --help|-h)
            usage
            exit 0
            ;;
        *)
            fail "unknown argument: $1"
            ;;
    esac
done

[[ "$binary_path" = /* ]] || fail "--binary must be an absolute path"
[[ -f "$binary_path" && -x "$binary_path" ]] || fail "dwsd binary is unavailable or not executable"
[[ "$git_sha" =~ ^[0-9a-f]{40}$ ]] || fail "--git-sha must be a full lowercase Git SHA"
[[ -n "$profile" && "$profile" != *','* && "$profile" != *$'\n'* && "$profile" != *$'\r'* ]] ||
    fail "--profile must identify one profile"
[[ -n "$verify_group" && -n "$verify_title" ]] || fail "verification group and title are required"

readonly service_root="${DWS_SERVICE_ROOT:-$DEFAULT_SERVICE_ROOT}"
readonly wrapper_path="${DWS_SERVICE_WRAPPER_PATH:-$DEFAULT_WRAPPER_PATH}"
readonly launch_agent="${DWS_LAUNCH_AGENT_PATH:-$DEFAULT_LAUNCH_AGENT}"
readonly profile_file="${service_root}/secrets/profile"
readonly token_file="${service_root}/secrets/http-token"
readonly releases_dir="${service_root}/releases"
readonly current_link="${service_root}/current"
readonly log_dir="${service_root}/logs"
readonly template_path="$(cd "$(dirname "$0")/.." && pwd)/ops/${LABEL}.plist.template"
readonly uid="$(id -u)"
readonly launch_domain="gui/${uid}"
readonly service_target="${launch_domain}/${LABEL}"

[[ "$service_root" = /* && "$launch_agent" = /* && "$wrapper_path" = /* ]] ||
    fail "service, LaunchAgent and wrapper paths must be absolute"
[[ -f "$wrapper_path" && -x "$wrapper_path" ]] || fail "trusted DWS wrapper is unavailable"
[[ -f "$template_path" ]] || fail "LaunchAgent template is unavailable"
[[ -f "$token_file" ]] || fail "DWS HTTP token file is unavailable"
[[ "$(stat -f '%Lp' "$token_file")" = "600" ]] || fail "DWS HTTP token file must have mode 600"

mkdir -p "$releases_dir" "$log_dir" "$(dirname "$launch_agent")" "${service_root}/tmp"

readonly release_dir="${releases_dir}/${git_sha}"
readonly release_binary="${release_dir}/dwsd"
if [[ -e "$release_dir" && ! -d "$release_dir" ]]; then
    fail "release path is not a directory"
fi
if [[ ! -d "$release_dir" ]]; then
    release_staging="$(mktemp -d "${releases_dir}/.${git_sha}.XXXXXX")"
    install -m 0555 "$binary_path" "${release_staging}/dwsd"
    printf '%s\n' "$git_sha" >"${release_staging}/git-revision"
    shasum -a 256 "${release_staging}/dwsd" | awk '{print $1}' >"${release_staging}/sha256"
    chmod 0444 "${release_staging}/git-revision" "${release_staging}/sha256"
    mv "$release_staging" "$release_dir"
fi
[[ -x "$release_binary" ]] || fail "immutable release binary is unavailable"

readonly deployed_binary_sha="$(shasum -a 256 "$release_binary" | awk '{print $1}')"
readonly recorded_binary_sha="$(<"${release_dir}/sha256")"
readonly source_binary_sha="$(shasum -a 256 "$binary_path" | awk '{print $1}')"
[[ "$deployed_binary_sha" = "$recorded_binary_sha" ]] || fail "immutable release checksum mismatch"
[[ "$deployed_binary_sha" = "$source_binary_sha" ]] || fail "existing release does not match the requested binary"

candidate_dir="$(mktemp -d "${service_root}/tmp/candidate.${git_sha}.XXXXXX")"
candidate_pid=""
cutover_started="false"
cutover_complete="false"
previous_link=""
previous_launch_loaded="false"
docker_container=""
docker_was_running="false"

cleanup_candidate() {
    if [[ -n "$candidate_pid" ]] && kill -0 "$candidate_pid" 2>/dev/null; then
        kill "$candidate_pid" 2>/dev/null || true
        wait "$candidate_pid" 2>/dev/null || true
    fi
    case "$candidate_dir" in
        "${service_root}/tmp/candidate.${git_sha}."*) rm -rf -- "$candidate_dir" ;;
    esac
}

replace_symlink() {
    local target=$1
    local link=$2
    local temporary="${link}.next.$$"
    rm -f -- "$temporary"
    ln -s "$target" "$temporary"
    python3 - "$temporary" "$link" <<'PY'
import os
import sys

os.replace(sys.argv[1], sys.argv[2])
PY
}

finalize() {
    local status=$?
    trap - EXIT
    set +e
    if [[ "$cutover_started" = "true" && "$cutover_complete" != "true" ]]; then
        printf '%s\n' "Cutover failed; restoring previous DWS service" >&2
        launchctl bootout "$service_target" >/dev/null 2>&1 || true
        if [[ -f "${candidate_dir}/profile.previous" ]]; then
            install -m 0600 "${candidate_dir}/profile.previous" "$profile_file"
        elif [[ -f "$profile_file" ]]; then
            rm -f -- "$profile_file"
        fi
        if [[ -n "$previous_link" ]]; then
            replace_symlink "$previous_link" "$current_link"
        else
            rm -f -- "$current_link"
        fi
        if [[ -f "${candidate_dir}/launch-agent.previous" ]]; then
            install -m 0644 "${candidate_dir}/launch-agent.previous" "$launch_agent"
        fi
        if [[ "$previous_launch_loaded" = "true" && -f "$launch_agent" ]]; then
            launchctl bootstrap "$launch_domain" "$launch_agent" >/dev/null 2>&1 || true
        elif [[ "$docker_was_running" = "true" && -n "$docker_container" ]]; then
            docker start "$docker_container" >/dev/null 2>&1 || true
        fi
    fi
    cleanup_candidate
    exit "$status"
}

trap finalize EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

printf '%s\n' "$profile" >"${candidate_dir}/profile"
chmod 0600 "${candidate_dir}/profile"

start_candidate() {
    env \
        HOME="/Users/yuanzhan" \
        USER="yuanzhan" \
        LOGNAME="yuanzhan" \
        LANG="en_US.UTF-8" \
        LC_ALL="en_US.UTF-8" \
        PATH="/usr/local/bin:/opt/homebrew/bin:/usr/bin:/bin:/usr/sbin:/sbin" \
        DWS_SERVICE_LISTEN_ADDR="127.0.0.1:8003" \
        DWS_SERVICE_PROFILE_FILE="${candidate_dir}/profile" \
        DWS_SERVICE_TOKEN_FILE="$token_file" \
        DWS_SERVICE_WRAPPER_PATH="$wrapper_path" \
        DWS_SERVICE_COMMAND_TIMEOUT="30s" \
        DWS_SERVICE_MAX_BODY_BYTES="1048576" \
        DWS_SERVICE_MAX_OUTPUT_BYTES="16777216" \
        "$release_binary" \
        >"${candidate_dir}/stdout.log" \
        2>"${candidate_dir}/stderr.log" &
    candidate_pid=$!
}

wait_ready() {
    local port=$1
    local attempts=60
    while ((attempts > 0)); do
        if curl --fail --silent --show-error --max-time 3 "http://127.0.0.1:${port}/readyz" >/dev/null 2>&1; then
            return 0
        fi
        attempts=$((attempts - 1))
        sleep 1
    done
    return 1
}

verify_http_contract() {
    local port=$1
    local token
    token="$(<"$token_file")"
    local schema_response="${candidate_dir}/schema-${port}.json"
    local identity_response="${candidate_dir}/identity-${port}.json"
    local group_response="${candidate_dir}/group-${port}.json"

    curl --fail --silent --show-error --max-time 10 \
        -H "Authorization: Bearer ${token}" \
        "http://127.0.0.1:${port}/v1/schema" \
        >"$schema_response"
    jq -e '.data.metadata.command_count == 6' "$schema_response" >/dev/null

    jq -n --arg profile "$profile" '{profile:$profile, arguments:{}}' |
        curl --fail --silent --show-error --max-time 35 \
            -H "Authorization: Bearer ${token}" \
            -H 'Content-Type: application/json' \
            --data-binary @- \
            "http://127.0.0.1:${port}/v1/commands/contact.get_current_user_profile/execute" \
            >"$identity_response"
    jq -e --arg profile "$profile" \
        '.data.content.success == true and .data.content.result[0].orgEmployeeModel.corpId == $profile' \
        "$identity_response" >/dev/null

    jq -n --arg profile "$profile" --arg group "$verify_group" \
        '{profile:$profile, arguments:{group:$group}}' |
        curl --fail --silent --show-error --max-time 35 \
            -H "Authorization: Bearer ${token}" \
            -H 'Content-Type: application/json' \
            --data-binary @- \
            "http://127.0.0.1:${port}/v1/commands/chat.get_conversation_info/execute" \
            >"$group_response"
    jq -e --arg profile "$profile" --arg title "$verify_title" \
        '.data.content.success == true and
         .data.content.result.conversationInfo.corpId == $profile and
         .data.content.result.conversationInfo.title == $title' \
        "$group_response" >/dev/null
}

start_candidate
wait_ready 8003 || fail "candidate DWS service did not become ready"
verify_http_contract 8003 || fail "candidate DWS HTTP contract verification failed"
kill "$candidate_pid"
wait "$candidate_pid" || true
candidate_pid=""

if [[ -L "$current_link" ]]; then
    previous_link="$(readlink "$current_link")"
elif [[ -e "$current_link" ]]; then
    fail "current release path exists but is not a symlink"
fi
if launchctl print "$service_target" >/dev/null 2>&1; then
    previous_launch_loaded="true"
fi
if [[ -f "$launch_agent" ]]; then
    cp -p "$launch_agent" "${candidate_dir}/launch-agent.previous"
fi
if [[ -f "$profile_file" ]]; then
    cp -p "$profile_file" "${candidate_dir}/profile.previous"
fi

docker_container="$(
    docker ps -aq \
        --filter 'label=com.docker.compose.project=dws' \
        --filter 'label=com.docker.compose.service=dws' |
        head -1
)"
if [[ -n "$docker_container" ]] && [[ "$(docker inspect -f '{{.State.Running}}' "$docker_container")" = "true" ]]; then
    docker_was_running="true"
fi

python3 - "$template_path" "${candidate_dir}/launch-agent.plist" \
    "$current_link" "$profile_file" "$token_file" "$wrapper_path" "$log_dir" <<'PY'
from pathlib import Path
import sys

source, target, current, profile, token, wrapper, logs = sys.argv[1:]
text = Path(source).read_text(encoding="utf-8")
replacements = {
    "__DWS_BINARY__": f"{current}/dwsd",
    "__DWS_WORKING_DIRECTORY__": current,
    "__DWS_HOME__": "/Users/yuanzhan",
    "__DWS_PROFILE_FILE__": profile,
    "__DWS_TOKEN_FILE__": token,
    "__DWS_WRAPPER_PATH__": wrapper,
    "__DWS_STDOUT_LOG__": f"{logs}/stdout.log",
    "__DWS_STDERR_LOG__": f"{logs}/stderr.log",
}
for placeholder, value in replacements.items():
    if placeholder not in text:
        raise SystemExit(f"missing plist placeholder: {placeholder}")
    text = text.replace(placeholder, value)
Path(target).write_text(text, encoding="utf-8")
PY
plutil -lint "${candidate_dir}/launch-agent.plist" >/dev/null

cutover_started="true"
launchctl bootout "$service_target" >/dev/null 2>&1 || true
if [[ "$docker_was_running" = "true" ]]; then
    docker stop --time 30 "$docker_container" >/dev/null
fi

install -m 0600 "${candidate_dir}/profile" "$profile_file"
replace_symlink "$release_dir" "$current_link"
install -m 0644 "${candidate_dir}/launch-agent.plist" "$launch_agent"
launchctl bootstrap "$launch_domain" "$launch_agent"

wait_ready 8002 || fail "deployed DWS LaunchAgent did not become ready"
verify_http_contract 8002 || fail "deployed DWS HTTP contract verification failed"

cutover_complete="true"
printf 'DWS_DEPLOYED_GIT_SHA=%s\n' "$git_sha"
printf 'DWS_DEPLOYED_BINARY_SHA256=%s\n' "$deployed_binary_sha"
printf 'DWS_DEPLOYED_PROFILE=%s\n' "$profile"
