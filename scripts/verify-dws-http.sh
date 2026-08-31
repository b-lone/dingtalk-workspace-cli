#!/usr/bin/env bash
set -Eeuo pipefail

umask 077

fail() {
    printf 'ERROR: %s\n' "$1" >&2
    exit 1
}

usage() {
    printf '%s\n' \
        "Usage: verify-dws-http.sh --base-url URL --token-file PATH" \
        "       --profile CORP_ID --verify-group OPEN_CONVERSATION_ID" \
        "       --verify-title TITLE"
}

base_url=""
token_file=""
profile=""
verify_group=""
verify_title=""

while (($# > 0)); do
    case "$1" in
        --base-url)
            (($# >= 2)) || fail "--base-url requires a value"
            base_url="$2"
            shift 2
            ;;
        --token-file)
            (($# >= 2)) || fail "--token-file requires a value"
            token_file="$2"
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

command -v curl >/dev/null 2>&1 || fail "curl is unavailable"
command -v jq >/dev/null 2>&1 || fail "jq is unavailable"

[[ "$base_url" =~ ^http://127\.0\.0\.1:[1-9][0-9]{0,4}$ ]] ||
    fail "--base-url must be an explicit 127.0.0.1 HTTP port"
[[ "$token_file" = /* && -f "$token_file" && -r "$token_file" ]] ||
    fail "--token-file must be an absolute readable file"
[[ -n "$profile" && "$profile" != *','* && "$profile" != *$'\n'* && "$profile" != *$'\r'* ]] ||
    fail "--profile must identify one profile"
[[ -n "$verify_group" && -n "$verify_title" ]] ||
    fail "verification group and title are required"

readonly token="$(<"$token_file")"
[[ -n "$token" ]] || fail "DWS HTTP token is empty"

readonly result_dir="$(mktemp -d "${TMPDIR:-/tmp}/dws-http-verify.XXXXXX")"
cleanup() {
    rm -rf -- "$result_dir"
}
trap cleanup EXIT

curl --fail --silent --show-error --max-time 5 \
    "${base_url}/healthz" >/dev/null
curl --fail --silent --show-error --max-time 10 \
    "${base_url}/readyz" >/dev/null

readonly schema_response="${result_dir}/schema.json"
readonly identity_response="${result_dir}/identity.json"
readonly group_response="${result_dir}/group.json"

curl --fail --silent --show-error --max-time 10 \
    -H "Authorization: Bearer ${token}" \
    "${base_url}/v1/schema" >"$schema_response"
jq -e '.data.metadata.command_count == 6' "$schema_response" >/dev/null ||
    fail "DWS Schema command count verification failed"

jq -n --arg profile "$profile" '{profile:$profile, arguments:{}}' |
    curl --fail --silent --show-error --max-time 35 \
        -H "Authorization: Bearer ${token}" \
        -H 'Content-Type: application/json' \
        --data-binary @- \
        "${base_url}/v1/commands/contact.get_current_user_profile/execute" \
        >"$identity_response"
jq -e --arg profile "$profile" \
    '.data.content.success == true and .data.content.result[0].orgEmployeeModel.corpId == $profile' \
    "$identity_response" >/dev/null ||
    fail "DWS profile identity verification failed"

jq -n --arg profile "$profile" --arg group "$verify_group" \
    '{profile:$profile, arguments:{group:$group}}' |
    curl --fail --silent --show-error --max-time 35 \
        -H "Authorization: Bearer ${token}" \
        -H 'Content-Type: application/json' \
        --data-binary @- \
        "${base_url}/v1/commands/chat.get_conversation_info/execute" \
        >"$group_response"
jq -e --arg profile "$profile" --arg title "$verify_title" \
    '.data.content.success == true and
     .data.content.result.conversationInfo.corpId == $profile and
     .data.content.result.conversationInfo.title == $title' \
    "$group_response" >/dev/null ||
    fail "DWS group verification failed"

printf 'DWS_VERIFIED_PROFILE=%s\n' "$profile"
printf 'DWS_VERIFIED_GROUP_TITLE=%s\n' "$verify_title"
