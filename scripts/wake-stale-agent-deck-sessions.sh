#!/usr/bin/env bash

set -euo pipefail

usage() {
  cat <<'EOF'
Usage:
  wake-stale-agent-deck-sessions.sh [options]
  wake-stale-agent-deck-sessions.sh install [options]

Purpose:
  Find agent-deck sessions that are idle or waiting and have stale unread
  mailbox items, then send a wakeup message only after a short confirm-delay
  recheck.

Options:
  --older-than DURATION       Required stale threshold for agent-mailbox stale
  --confirm-delay SECONDS     Delay before rechecking a candidate session (default: 2)
  --profile NAME             Agent-deck profile to query
  --state-dir PATH           Mailbox state directory to query
  --wake-message TEXT        Ignored; agent-deck wake instruction is fixed
  --all-mail-states          Pass --all to agent-deck list (include sessions hidden by default)
  -h, --help                 Show help

Install options:
  --prefix PATH              Install under PATH/bin (default: $HOME/.local)
  --bin-dir PATH             Install directly into this directory
  --name NAME                Installed filename (default: wake-stale-agent-deck-sessions.sh)

Exit codes:
  0  Success, or no sessions needed waking
  2  Usage or dependency error
  1  Operational failure while querying or waking one or more sessions
EOF
}

die() {
  printf 'ERROR: %s\n' "$*" >&2
  exit 2
}

warn() {
  printf 'WARN: %s\n' "$*" >&2
}

info() {
  printf 'INFO: %s\n' "$*" >&2
}

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || die "$1 not found in PATH"
}

script_source_path() {
  local source_dir
  source_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
  printf '%s/%s\n' "$source_dir" "$(basename "${BASH_SOURCE[0]}")"
}

install_self() {
  local prefix_dir="$1"
  local bin_dir="$2"
  local install_name="$3"
  local source_path target_path

  source_path="$(script_source_path)"
  target_path="${bin_dir%/}/${install_name}"

  if [[ -e "$source_path" && -e "$target_path" && "$source_path" == "$target_path" ]]; then
    info "already installed at $target_path"
    return 0
  fi

  if command -v install >/dev/null 2>&1; then
    mkdir -p "$bin_dir"
    install -m 0755 "$source_path" "$target_path"
  else
    mkdir -p "$bin_dir"
    cp "$source_path" "$target_path"
    chmod 0755 "$target_path"
  fi

  info "installed $install_name to $target_path"
}

ad() {
  if [[ -n "$profile" ]]; then
    agent-deck -p "$profile" "$@"
  else
    agent-deck "$@"
  fi
}

if [[ "${1:-}" == "install" ]]; then
  shift

  prefix_dir="${HOME}/.local"
  bin_dir=""
  install_name="wake-stale-agent-deck-sessions.sh"

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --prefix) prefix_dir="${2:-}"; shift 2 ;;
      --bin-dir) bin_dir="${2:-}"; shift 2 ;;
      --name) install_name="${2:-}"; shift 2 ;;
      -h|--help) usage; exit 0 ;;
      *) die "unknown install option: $1" ;;
    esac
  done

  if [[ -z "$bin_dir" ]]; then
    [[ -n "$prefix_dir" ]] || die "--prefix is required when --bin-dir is not provided"
    bin_dir="${prefix_dir%/}/bin"
  fi
  [[ -n "$bin_dir" ]] || die "--bin-dir is required"
  [[ -n "$install_name" ]] || die "--name is required"

  install_self "$prefix_dir" "$bin_dir" "$install_name"
  exit 0
fi

mailbox() {
  if [[ -n "$state_dir" ]]; then
    agent-mailbox --state-dir "$state_dir" "$@"
  else
    agent-mailbox "$@"
  fi
}

session_status_is_wakeable() {
  case "${1:-}" in
    waiting|idle) return 0 ;;
    *) return 1 ;;
  esac
}

list_sessions_json() {
  if (( list_all )); then
    ad list --all --json
  else
    ad list --json
  fi
}

extract_candidate_sessions() {
  jq -r '
    def sessions:
      if type == "array" then
        .
      elif type == "object" and has("sessions") then
        .sessions
      elif type == "object" and has("items") then
        .items
      else
        []
      end;

    sessions
    | .[]
    | select(((.status // "") | ascii_downcase) as $status | ($status == "waiting" or $status == "idle"))
    | select((.id // "") != "")
    | [.id, (.status // ""), (.title // "")] | @tsv
  '
}

older_than=""
confirm_delay_seconds=2
profile=""
state_dir=""
readonly fixed_wake_message="Use the check-agent-mail skill now. Receive the pending message for your current agent-deck session and execute its requested action."
list_all=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    --older-than) older_than="${2:-}"; shift 2 ;;
  --confirm-delay) confirm_delay_seconds="${2:-}"; shift 2 ;;
  --profile) profile="${2:-}"; shift 2 ;;
  --state-dir) state_dir="${2:-}"; shift 2 ;;
  --wake-message) shift 2 ;;
  --all-mail-states) list_all=1; shift 1 ;;
  -h|--help) usage; exit 0 ;;
  *) die "unknown option: $1" ;;
  esac
done

[[ -n "$older_than" ]] || die "--older-than is required"
[[ "$confirm_delay_seconds" =~ ^[0-9]+$ ]] || die "--confirm-delay must be a non-negative integer"

require_cmd agent-deck
require_cmd agent-mailbox
require_cmd jq
require_cmd sleep

active_ids=()
declare -A active_status_by_id=()
declare -A active_title_by_id=()

set +e
sessions_json="$(list_sessions_json 2>&1)"
sessions_status=$?
set -e
if (( sessions_status != 0 )); then
  die "agent-deck list failed: $sessions_json"
fi

while IFS=$'\t' read -r session_id session_status session_title; do
  [[ -n "$session_id" ]] || continue
  active_ids+=("$session_id")
  active_status_by_id["$session_id"]="$session_status"
  active_title_by_id["$session_id"]="$session_title"
done < <(extract_candidate_sessions <<<"$sessions_json")

if (( ${#active_ids[@]} == 0 )); then
  info "no idle/waiting sessions found"
  exit 0
fi

stale_addresses=()
declare -A stale_count_by_address=()

stale_args=(stale --older-than "$older_than" --json)
for session_id in "${active_ids[@]}"; do
  stale_args+=(--for "agent-deck/${session_id}")
done

set +e
stale_json="$(mailbox "${stale_args[@]}" 2>&1)"
stale_status=$?
set -e
if (( stale_status != 0 )); then
  die "agent-mailbox stale failed: $stale_json"
fi

while IFS=$'\t' read -r address oldest_eligible_at claimable_count; do
  [[ -n "$address" ]] || continue
  stale_addresses+=("$address")
  stale_count_by_address["$address"]="$claimable_count"
done < <(
  jq -r '.[] | [.address, .oldest_eligible_at, (.claimable_count | tostring)] | @tsv' <<<"$stale_json"
)

if (( ${#stale_addresses[@]} == 0 )); then
  info "no stale unread mail found for active sessions"
  exit 0
fi

failures=0
woken=0
skipped_status=0
skipped_fresh=0

for address in "${stale_addresses[@]}"; do
  session_id="${address#agent-deck/}"
  original_status="${active_status_by_id[$session_id]:-unknown}"
  title="${active_title_by_id[$session_id]:-}"
  stale_count="${stale_count_by_address[$address]}"

  info "candidate session_id=${session_id} status=${original_status} title=${title:-none} stale_count=${stale_count} confirm_delay=${confirm_delay_seconds}s"
  sleep "$confirm_delay_seconds"

  set +e
  session_json="$(ad session show "$session_id" --json 2>&1)"
  session_status=$?
  set -e
  if (( session_status != 0 )); then
    warn "skip session_id=${session_id} reason=session_show_failed details=${session_json}"
    failures=1
    continue
  fi

  current_status="$(jq -r '.status // empty' <<<"$session_json")"
  if ! session_status_is_wakeable "$current_status"; then
    printf 'skip session_id=%s reason=status_changed current_status=%s stale_count=%s\n' \
      "$session_id" "${current_status:-unknown}" "$stale_count"
    skipped_status=$((skipped_status + 1))
    continue
  fi

  set +e
  current_stale_json="$(mailbox stale --for "$address" --older-than "$older_than" --json 2>&1)"
  current_stale_status=$?
  set -e
  if (( current_stale_status != 0 )); then
    warn "skip session_id=${session_id} reason=stale_recheck_failed details=${current_stale_json}"
    failures=1
    continue
  fi
  current_stale_count="$(jq -r 'length' <<<"$current_stale_json")"
  if [[ "$current_stale_count" == "0" ]]; then
    printf 'skip session_id=%s reason=mail_no_longer_stale current_status=%s\n' \
      "$session_id" "${current_status:-unknown}"
    skipped_fresh=$((skipped_fresh + 1))
    continue
  fi

  set +e
  wake_output="$(ad session send --no-wait "$session_id" "$fixed_wake_message" 2>&1)"
  wake_status=$?
  set -e
  if (( wake_status != 0 )); then
    warn "wake failed for session_id=${session_id}: ${wake_output}"
    failures=1
    continue
  fi

  printf 'woke session_id=%s status=%s stale_count=%s\n' \
    "$session_id" "$current_status" "$current_stale_count"
  woken=$((woken + 1))
done

printf 'summary active=%d stale=%d woken=%d skipped_status=%d skipped_fresh=%d failures=%d\n' \
  "${#active_ids[@]}" \
  "${#stale_addresses[@]}" \
  "$woken" \
  "$skipped_status" \
  "$skipped_fresh" \
  "$failures"

if (( failures != 0 )); then
  exit 1
fi
