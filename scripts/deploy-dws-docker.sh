#!/usr/bin/env bash
set -Eeuo pipefail

umask 077

fail() {
    printf 'ERROR: %s\n' "$1" >&2
    exit 1
}

usage() {
    printf '%s\n' \
        "Usage: deploy-dws-docker.sh --deploy-script PATH --compose-file PATH" \
        "       --image-ref DIGEST_REF --registry-host HOST" \
        "       --channel-code CHANNEL_CODE --profile CORP_ID" \
        "       --token-file PATH --base-url URL" \
        "       --verify-group OPEN_CONVERSATION_ID --verify-title TITLE"
}

deploy_script=""
compose_file=""
image_ref=""
registry_host=""
channel_code=""
profile=""
token_file=""
base_url=""
verify_group=""
verify_title=""

while (($# > 0)); do
    case "$1" in
        --deploy-script)
            (($# >= 2)) || fail "--deploy-script requires a value"
            deploy_script="$2"
            shift 2
            ;;
        --compose-file)
            (($# >= 2)) || fail "--compose-file requires a value"
            compose_file="$2"
            shift 2
            ;;
        --image-ref)
            (($# >= 2)) || fail "--image-ref requires a value"
            image_ref="$2"
            shift 2
            ;;
        --registry-host)
            (($# >= 2)) || fail "--registry-host requires a value"
            registry_host="$2"
            shift 2
            ;;
        --channel-code)
            (($# >= 2)) || fail "--channel-code requires a value"
            channel_code="$2"
            shift 2
            ;;
        --profile)
            (($# >= 2)) || fail "--profile requires a value"
            profile="$2"
            shift 2
            ;;
        --token-file)
            (($# >= 2)) || fail "--token-file requires a value"
            token_file="$2"
            shift 2
            ;;
        --base-url)
            (($# >= 2)) || fail "--base-url requires a value"
            base_url="$2"
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

readonly project="dws"
readonly verify_script="$(cd "$(dirname "$0")" && pwd)/verify-dws-http.sh"
readonly state_root="${DEPLOY_IMAGE_STATE_DIR:-/Users/yuanzhan/Documents/Data/deploy_images}"
readonly state_file="${state_root}/${project}/image.env"

is_immutable_dws_image() {
    local candidate_ref="$1"
    local prefix="${registry_host}:5443/dws@sha256:"
    local digest

    [[ "$candidate_ref" = "$prefix"* ]] || return 1
    digest="${candidate_ref#"$prefix"}"
    [[ "$digest" =~ ^[0-9a-f]{64}$ ]]
}

[[ "$deploy_script" = /* && -f "$deploy_script" && -x "$deploy_script" ]] ||
    fail "--deploy-script must be an absolute executable file"
[[ "$compose_file" = /* && -f "$compose_file" ]] ||
    fail "--compose-file must be an absolute file"
[[ -x "$verify_script" ]] || fail "DWS HTTP verification script is unavailable"
[[ "$registry_host" =~ ^[A-Za-z0-9][A-Za-z0-9.-]*$ ]] ||
    fail "--registry-host is invalid"
is_immutable_dws_image "$image_ref" ||
    fail "--image-ref must be an immutable dws digest from the selected registry"
[[ "$channel_code" =~ ^[A-Za-z0-9_-]{1,128}$ ]] ||
    fail "--channel-code must match [A-Za-z0-9_-]{1,128}"

previous_image_ref=""
if [[ -f "$state_file" ]]; then
    state_image_count="$(grep -c '^IMAGE_REF=' "$state_file" || true)"
    [[ "$state_image_count" = "1" ]] ||
        fail "current Docker image state must contain exactly one IMAGE_REF"
    previous_image_ref="$(sed -n 's/^IMAGE_REF=//p' "$state_file")"
    is_immutable_dws_image "$previous_image_ref" ||
        fail "current Docker image state is not an immutable dws digest"
fi

verify_service() {
    "$verify_script" \
        --base-url "$base_url" \
        --token-file "$token_file" \
        --profile "$profile" \
        --verify-group "$verify_group" \
        --verify-title "$verify_title"
}

export DWS_CHANNEL="$channel_code"

"$deploy_script" \
    "$project" \
    "$compose_file" \
    "$image_ref" \
    "$registry_host"

if verify_service; then
    printf 'DWS_DEPLOYED_IMAGE_REF=%s\n' "$image_ref"
    exit 0
fi

printf '%s\n' "Candidate Docker image failed DWS business verification" >&2
[[ -n "$previous_image_ref" && "$previous_image_ref" != "$image_ref" ]] ||
    fail "no previous Docker image is available for release rollback"

"$deploy_script" \
    "$project" \
    "$compose_file" \
    "$previous_image_ref" \
    "$registry_host" ||
    fail "candidate verification failed and previous Docker image deployment failed"

verify_service ||
    fail "candidate verification failed and restored Docker image failed business verification"

printf 'ERROR: candidate Docker image failed verification; restored previous Docker image: %s\n' \
    "$previous_image_ref" >&2
exit 1
