#!/usr/bin/env bash
# 离线验证安装器；所有下载和 systemd 调用均为本进程中的替身。
set -euo pipefail

script_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)
source "$script_root/install-komari.sh"
test_root=$(mktemp -d "${TMPDIR:-/tmp}/komari-server-installer.XXXXXX")
test_root=$(cd -- "$test_root" && pwd -P)
case "$test_root" in /*/komari-server-installer.*) ;; *) exit 1 ;; esac
trap 'rm -rf -- "$test_root"' EXIT

MODE=success
RELEASE_TAG=1.5.1
NEW_BINARY=owned-binary-fixture
OLD_BINARY=original-binary-fixture
EXPECTED_HASH=$(printf '%s' "$NEW_BINARY" | sha256sum)
EXPECTED_HASH=${EXPECTED_HASH%% *}

log_info() { :; }
log_error() { :; }
log_success() { :; }
log_step() { :; }
ui_msgbox() { :; }
ui_input() { printf '25774\n'; }
install_dependencies() { :; }
select_channel() { :; }
detect_arch() { printf 'amd64\n'; }
sleep() { :; }
create_systemd_service() { printf 'create-service\n' >> "$EVENTS"; }
show_access_info() { :; }

curl() {
    local url=${!#} output='' previous='' argument head_request=0
    for argument in "$@"; do
        [ "$previous" != -o ] || output=$argument
        case "$argument" in -*I*) head_request=1 ;; esac
        previous=$argument
    done
    printf '%s\n' "$url" >> "$DOWNLOADS"
    case "$url" in
        https://api.github.com/repos/R1ddle1337/komari/releases/latest)
            [ "$MODE" != api_failure ] || return 22
            printf '{"tag_name":"%s"}\n' "$RELEASE_TAG"
            ;;
        'https://api.github.com/repos/R1ddle1337/komari/releases?per_page=100')
            printf '[{"tag_name":"1.5.1"},{"tag_name":"Snapshot-260922010000-123-1"}]\n'
            ;;
        https://github.com/R1ddle1337/komari/releases/download/*/komari-linux-amd64.sha256)
            [ "$MODE" != missing_checksum ] || return 22
            case "$MODE" in
                bad_checksum) printf '%064d  komari-linux-amd64\n' 0 > "$output" ;;
                wrong_filename) printf '%s  another-binary\n' "$EXPECTED_HASH" > "$output" ;;
                short_checksum) printf 'short\n' > "$output" ;;
                duplicate_checksum) printf '%s  komari-linux-amd64\n%s  komari-linux-amd64\n' "$EXPECTED_HASH" "$EXPECTED_HASH" > "$output" ;;
                *) printf '%s  komari-linux-amd64\n' "$EXPECTED_HASH" > "$output" ;;
            esac
            ;;
        https://github.com/R1ddle1337/komari/releases/download/*/komari-linux-amd64)
            if [ "$head_request" -eq 1 ]; then
                printf 'Content-Length: %s\n' "${#NEW_BINARY}"
            else
                [ "$MODE" != download_failure ] || return 22
                printf '%s' "$NEW_BINARY" > "$output"
            fi
            ;;
        *) printf 'Unexpected network destination: %s\n' "$url" >&2; return 99 ;;
    esac
}

systemctl() {
    printf '%s\n' "$*" >> "$EVENTS"
    case "$1" in
        stop) [ "$MODE" != stop_failure ] ;;
        start)
            if [ "$MODE" = start_failure ] && [ "$(< "$BINARY_PATH")" = "$NEW_BINARY" ]; then return 1; fi
            ;;
        is-active)
            if [ "$MODE" = inactive_after_start ] && [ "$(< "$BINARY_PATH")" = "$NEW_BINARY" ]; then return 1; fi
            ;;
    esac
}

cp() {
    local destination=${!#}
    if [ "$MODE" = backup_failure ] && [[ "$destination" == "$BINARY_PATH".backup.* ]]; then return 1; fi
    command cp "$@"
}

mv() {
    local arguments=("$@") destination=${!#} source
    source=${arguments[${#arguments[@]}-2]}
    if [ "$MODE" = replacement_failure ] && [[ "$source" == */.komari-install.*/komari ]] && [ "$destination" = "$BINARY_PATH" ]; then return 1; fi
    command mv "$@"
}

prepare_case() {
    local name=$1
    INSTALL_DIR="$test_root/$name"
    DATA_DIR="$INSTALL_DIR"
    BINARY_PATH="$INSTALL_DIR/komari"
    EVENTS="$INSTALL_DIR/events"
    DOWNLOADS="$INSTALL_DIR/downloads"
    mkdir -p "$INSTALL_DIR"
    : > "$EVENTS"
    : > "$DOWNLOADS"
    printf '%s' "$OLD_BINARY" > "$BINARY_PATH"
    MODE=$name
    CHANNEL=stable
    CHANNEL_NAME=stable
    RELEASE_TAG=1.5.1
}

assert_no_staging() {
    if find "$INSTALL_DIR" -maxdepth 1 -name '.komari-install.*' | grep -q .; then
        printf 'Temporary installation directory leaked: %s\n' "$MODE" >&2
        exit 1
    fi
}

for failure in api_failure download_failure missing_checksum bad_checksum wrong_filename short_checksum duplicate_checksum backup_failure; do
    prepare_case "$failure"
    if upgrade_komari >/dev/null; then printf 'Unexpected successful upgrade: %s\n' "$failure" >&2; exit 1; fi
    [ "$(< "$BINARY_PATH")" = "$OLD_BINARY" ]
    [ ! -s "$EVENTS" ]
    assert_no_staging
done

prepare_case success
printf 'historical-binary' > "$BINARY_PATH.backup.earlier"
upgrade_komari >/dev/null
[ "$(< "$BINARY_PATH")" = "$NEW_BINARY" ]
grep -q '^stop komari.service$' "$EVENTS"
grep -q '^start komari.service$' "$EVENTS"
backups=("$BINARY_PATH".backup.*)
[ "${#backups[@]}" -eq 2 ]
[ "$(< "$BINARY_PATH.backup.earlier")" = historical-binary ]
for backup in "${backups[@]}"; do
    if [ "$backup" != "$BINARY_PATH.backup.earlier" ]; then [ "$(< "$backup")" = "$OLD_BINARY" ]; fi
done
grep -q '/releases/download/1.5.1/komari-linux-amd64.sha256$' "$DOWNLOADS"
! grep -q '/latest/download/' "$DOWNLOADS"
assert_no_staging

for failure in replacement_failure start_failure inactive_after_start stop_failure; do
    prepare_case "$failure"
    if upgrade_komari >/dev/null; then printf 'Failure was not reported: %s\n' "$failure" >&2; exit 1; fi
    [ "$(< "$BINARY_PATH")" = "$OLD_BINARY" ]
    backups=("$BINARY_PATH".backup.*)
    [ "$(< "${backups[0]}")" = "$OLD_BINARY" ]
    if [ "$failure" != stop_failure ]; then grep -q '^start komari.service$' "$EVENTS"; fi
    assert_no_staging
done

prepare_case snapshot
CHANNEL=snapshot
snapshot_url=$(get_download_url amd64)
[ "$snapshot_url" = 'https://github.com/R1ddle1337/komari/releases/download/Snapshot-260922010000-123-1/komari-linux-amd64' ]
CHANNEL=stable
RELEASE_TAG='../other-release'
if get_download_url amd64 >/dev/null; then printf 'Unsafe release tag accepted\n' >&2; exit 1; fi
REPO=komari-monitor/komari
if get_download_url amd64 >/dev/null; then printf 'External release repository accepted\n' >&2; exit 1; fi
REPO=$STANDARD_REPO

for mode in success bad_checksum; do
    prepare_case "fresh-$mode"
    MODE=$mode
    command rm -- "$BINARY_PATH"
    if [ "$mode" = success ]; then
        install_binary >/dev/null
        [ "$(< "$BINARY_PATH")" = "$NEW_BINARY" ]
        grep -q '^create-service$' "$EVENTS"
    else
        if install_binary >/dev/null; then printf 'Invalid fresh install succeeded\n' >&2; exit 1; fi
        [ ! -e "$BINARY_PATH" ]
        [ ! -s "$EVENTS" ]
    fi
    assert_no_staging
done

printf 'Installer offline checks passed: owned pinned releases, checksum failures, untouched running service, atomic replacement, backups, rollback, and fresh install.\n'
