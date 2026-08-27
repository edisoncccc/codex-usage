#!/usr/bin/env bash
if [[ "${GITHUB_ACTIONS:-}" != "true" || "${CI:-}" != "true" ||
      "${RUNNER_ENVIRONMENT:-}" != "github-hosted" || -z "${RUNNER_TEMP:-}" ||
      "$RUNNER_TEMP" != /* || ! -d "$RUNNER_TEMP" ||
      "$RUNNER_TEMP" != "$(cd -- "$RUNNER_TEMP" 2>/dev/null && pwd -P)" ]]; then
  printf '%s\n' 'This lifecycle test is restricted to a canonical GitHub-hosted RUNNER_TEMP.' >&2
  exit 1
fi

set -euo pipefail

runner_temp="$RUNNER_TEMP"
state_root="$(mktemp -d "$runner_temp/codex-usage-lifecycle-state.XXXXXX")"
codex_home="$(mktemp -d "$runner_temp/codex-usage-lifecycle-codex.XXXXXX")"
xdg_data="$(mktemp -d "$runner_temp/codex-usage-lifecycle-xdg-data.XXXXXX")"
xdg_config="$(mktemp -d "$runner_temp/codex-usage-lifecycle-xdg-config.XXXXXX")"
build_root="$(mktemp -d "$runner_temp/codex-usage-lifecycle-build.XXXXXX")"
temp_root="$(mktemp -d "$runner_temp/codex-usage-lifecycle-temp.XXXXXX")"

export CODEX_USAGE_HOME="$state_root"
export CODEX_HOME="$codex_home"
export XDG_DATA_HOME="$xdg_data"
export XDG_CONFIG_HOME="$xdg_config"
export TMPDIR="$temp_root"

fail() {
  printf 'lifecycle failure: %s\n' "$*" >&2
  exit 1
}

assert_under_runner_temp() {
  local path="$1" canonical
  [[ "$path" == /* ]] || fail "receipt path is not absolute: $path"
  canonical="$(realpath -m -- "$path")"
  case "$canonical" in
    "$runner_temp"/*) ;;
    *) fail "receipt path escaped RUNNER_TEMP: $canonical" ;;
  esac
}

json_get() {
  local document="$1" path="$2"
  python3 - "$document" "$path" <<'PY'
import json
import sys

value = json.loads(sys.argv[1])
for key in sys.argv[2].split('.'):
    value = value[key]
if isinstance(value, bool):
    print('true' if value else 'false')
elif value is None:
    print('null')
elif isinstance(value, (dict, list)):
    print(json.dumps(value, separators=(',', ':')))
else:
    print(value)
PY
}

command_counter=0
json_terminal=''
json_events_file=''
run_jsonl() {
  local executable="$1"
  shift
  command_counter=$((command_counter + 1))
  local stdout_file="$temp_root/jsonl-$command_counter.stdout"
  local stderr_file="$temp_root/jsonl-$command_counter.stderr"
  local status
  set +e
  "$executable" "$@" >"$stdout_file" 2>"$stderr_file"
  status=$?
  set -e
  if [[ $status -ne 0 ]]; then
    printf 'stderr:\n' >&2
    sed -n '1,120p' "$stderr_file" >&2
    printf 'stdout:\n' >&2
    sed -n '1,120p' "$stdout_file" >&2
    fail "JSON Lines command exited $status: $executable $*"
  fi
  [[ ! -s "$stderr_file" ]] || fail "successful JSON Lines command wrote stderr"
  json_terminal="$(python3 - "$stdout_file" <<'PY'
import json
import re
import sys

events = []
with open(sys.argv[1], encoding='utf-8') as stream:
    for number, raw in enumerate(stream, 1):
        if not raw.strip():
            continue
        try:
            event = json.loads(raw)
        except json.JSONDecodeError as error:
            raise SystemExit(f'line {number} is not JSON: {error}')
        for field in ('schema_version', 'event', 'phase', 'status', 'timestamp'):
            if field not in event or event[field] in ('', None):
                raise SystemExit(f'line {number} misses stable field {field}')
        if event['schema_version'] != '1':
            raise SystemExit(f'line {number} has schema_version {event["schema_version"]!r}')
        if not re.fullmatch(r'\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?Z', event['timestamp']):
            raise SystemExit(f'line {number} timestamp is not RFC3339 UTC')
        events.append(event)
if not events:
    raise SystemExit('machine command emitted no events')
terminal = [event for event in events if event['event'] in ('result', 'error')]
if len(terminal) != 1 or events[-1] is not terminal[0]:
    raise SystemExit('machine command must emit exactly one final terminal event')
if terminal[0]['event'] != 'result' or terminal[0]['status'] != 'success' or not terminal[0].get('code'):
    raise SystemExit('machine command did not finish successfully')
print(json.dumps(terminal[0], separators=(',', ':')))
PY
)" || fail "invalid JSON Lines schema"
  json_events_file="$stdout_file"
}

object_json=''
run_object() {
  local executable="$1"
  shift
  command_counter=$((command_counter + 1))
  local stdout_file="$temp_root/object-$command_counter.stdout"
  local stderr_file="$temp_root/object-$command_counter.stderr"
  "$executable" "$@" >"$stdout_file" 2>"$stderr_file" || {
    sed -n '1,120p' "$stderr_file" >&2
    fail "JSON object command failed: $executable $*"
  }
  [[ ! -s "$stderr_file" ]] || fail "successful JSON object command wrote stderr"
  python3 - "$stdout_file" <<'PY'
import json
import sys
with open(sys.argv[1], encoding='utf-8') as stream:
    json.load(stream)
PY
  object_json="$(cat "$stdout_file")"
}

assert_receipt_paths() {
  local terminal="$1" key value
  for key in install_path state_path database_path; do
    value="$(json_get "$terminal" "result.$key")"
    assert_under_runner_temp "$value"
  done
}

service_mode=''
assert_doctor_healthy() {
  local executable="$1" expected_version="$2" version doctor_status identity_matches path home
  run_jsonl "$executable" version --json
  version="$(json_get "$json_terminal" result.version)"
  [[ "$version" == "$expected_version" ]] || fail "installed version is $version, want $expected_version"
  [[ "$(json_get "$json_terminal" result.dirty)" == false ]] || fail "host lifecycle build is unexpectedly dirty"

  run_jsonl "$executable" doctor --json # doctor --json
  doctor_status="$(json_get "$json_terminal" result.status)"
  [[ "$doctor_status" != error ]] || fail "doctor reported an error"
  identity_matches="$(python3 - "$json_terminal" <<'PY'
import json
import sys
event = json.loads(sys.argv[1])
matches = [item for item in event['result']['checks']
           if item.get('name') == 'service_identity' and item.get('code') == 'identity_match']
print(len(matches))
PY
)"
  [[ "$identity_matches" == 1 ]] || fail "doctor did not prove one service identity"
  for path in state_dir config_path database_path install_dir executable; do
    assert_under_runner_temp "$(json_get "$json_terminal" "result.paths.$path")"
  done
  while IFS= read -r home; do
    [[ -z "$home" ]] || assert_under_runner_temp "$home"
  done < <(python3 - "$json_terminal" <<'PY'
import json
import sys
for home in json.loads(sys.argv[1])['result']['homes']:
    print(home)
PY
)
}

stop_test_service() {
  local expected_executable="$1" mode="$2" pid_path="$state_root/codex-usage.pid" service_pid observed attempt
  [[ -f "$pid_path" ]] || fail "test service PID file is missing"
  service_pid="$(tr -d '[:space:]' <"$pid_path")"
  [[ "$service_pid" =~ ^[1-9][0-9]*$ && "$service_pid" != "$$" ]] || fail "invalid test service PID"
  observed="$(readlink -f "/proc/$service_pid/exe")"
  [[ "$observed" == "$(realpath -m -- "$expected_executable")" ]] || fail "PID executable mismatch: $observed"
  if [[ "$mode" == persistent ]]; then
    systemctl --user stop codex-usage.service
  elif [[ "$mode" == detached_fallback ]]; then
    kill -TERM "$service_pid"
  else
    fail "unexpected service_mode: $mode"
  fi
  for attempt in $(seq 1 75); do
    if ! kill -0 "$service_pid" 2>/dev/null; then
      return
    fi
    sleep 0.2
  done
  fail "timed out stopping test service PID $service_pid"
}

wait_path_absent() {
  local path="$1" attempt
  for attempt in $(seq 1 120); do
    [[ ! -e "$path" ]] && return
    sleep 0.25
  done
  fail "timed out waiting for removal: $path"
}

command -v go >/dev/null || fail 'Go is unavailable'
command -v python3 >/dev/null || fail 'python3 is unavailable'
command -v realpath >/dev/null || fail 'realpath is unavailable'
commit="$(git rev-parse HEAD)"
[[ "$commit" =~ ^[0-9a-f]{40}$ ]] || fail 'cannot resolve a full source commit'
module='github.com/zJay26/codex-usage/internal/app'
build_date='2026-08-27T00:00:00Z'
old_binary="$build_root/codex-usage-old"
new_binary="$build_root/codex-usage-new"

CGO_ENABLED=0 go build -trimpath -buildvcs=false \
  -ldflags "-s -w -X $module.Version=2.3.5 -X $module.Commit=$commit -X $module.BuildDirty=false -X $module.BuildDate=$build_date" \
  -o "$old_binary" ./cmd/codex-usage
CGO_ENABLED=0 go build -trimpath -buildvcs=false \
  -ldflags "-s -w -X $module.Version=2.3.6 -X $module.Commit=$commit -X $module.BuildDirty=false -X $module.BuildDate=$build_date" \
  -o "$new_binary" ./cmd/codex-usage

session_dir="$codex_home/sessions/2026/08/27"
mkdir -p "$session_dir"
cat >"$session_dir/rollout-synthetic-lifecycle.jsonl" <<'JSONL'
{"timestamp":"2026-08-27T00:00:00Z","type":"session_meta","payload":{"id":"synthetic-lifecycle-session","cwd":"/synthetic/project","originator":"codex_cli_rs"}}
{"timestamp":"2026-08-27T00:00:01Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":20,"cached_input_tokens":10,"cache_write_input_tokens":0,"output_tokens":4,"reasoning_output_tokens":1,"total_tokens":24},"last_token_usage":{"input_tokens":20,"cached_input_tokens":10,"cache_write_input_tokens":0,"output_tokens":4,"reasoning_output_tokens":1,"total_tokens":24}}}}
{"timestamp":"2026-08-27T00:00:02Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":120,"cached_input_tokens":80,"cache_write_input_tokens":0,"output_tokens":24,"reasoning_output_tokens":5,"total_tokens":144},"last_token_usage":{"input_tokens":100,"cached_input_tokens":70,"cache_write_input_tokens":0,"output_tokens":20,"reasoning_output_tokens":4,"total_tokens":120}}}}
JSONL

port="$(python3 - <<'PY'
import socket
with socket.socket() as listener:
    listener.bind(('127.0.0.1', 0))
    print(listener.getsockname()[1])
PY
)"
config_path="$state_root/config.json"
cat >"$config_path" <<JSON
{
  "listen_address": "127.0.0.1",
  "port": $port,
  "scan_interval_seconds": 600,
  "extra_codex_homes": [],
  "pricing_overrides": {}
}
JSON

if command -v systemctl >/dev/null && systemctl --user show-environment >/dev/null 2>&1; then
  systemctl --user import-environment CODEX_USAGE_HOME CODEX_HOME XDG_DATA_HOME XDG_CONFIG_HOME
fi

printf '%s\n' 'Scenario 1: fresh old identity install'
run_jsonl "$old_binary" install --yes --json # install --yes --json
old_install_terminal="$json_terminal"
assert_receipt_paths "$old_install_terminal"
[[ "$(json_get "$old_install_terminal" result.identity.version)" == 2.3.5 ]] || fail 'fresh identity mismatch'
service_mode="$(json_get "$old_install_terminal" result.service_mode)"
[[ "$service_mode" == persistent || "$service_mode" == detached_fallback ]] || fail "unexpected service mode: $service_mode"
installed_binary="$(json_get "$old_install_terminal" result.install_path)"
database_path="$(json_get "$old_install_terminal" result.database_path)"
unit_path="$xdg_config/systemd/user/codex-usage.service"
[[ -f "$unit_path" ]] || fail 'Linux install did not create an isolated user unit'
[[ -f "$database_path" ]] || fail 'fresh install did not create the isolated database'
assert_doctor_healthy "$installed_binary" 2.3.5

printf '%s\n' 'Scenario 2: idempotent same-binary install'
installed_sha256="$(sha256sum "$installed_binary" | awk '{print $1}')"
record_path="$state_root/install.json"
record_sha256="$(sha256sum "$record_path" | awk '{print $1}')"
run_jsonl "$old_binary" install --yes --json # install --yes --json
assert_receipt_paths "$json_terminal"
[[ "$(sha256sum "$installed_binary" | awk '{print $1}')" == "$installed_sha256" ]] || fail 'idempotent install changed executable sha256'
[[ "$(sha256sum "$record_path" | awk '{print $1}')" == "$record_sha256" ]] || fail 'idempotent install changed record sha256'
service_mode="$(json_get "$json_terminal" result.service_mode)"
assert_doctor_healthy "$installed_binary" 2.3.5

printf '%s\n' 'Scenario 3: stopped service repair'
stop_test_service "$installed_binary" "$service_mode"
run_jsonl "$old_binary" install --yes --json # install --yes --json
service_mode="$(json_get "$json_terminal" result.service_mode)"
assert_doctor_healthy "$installed_binary" 2.3.5

config_sha256="$(sha256sum "$config_path" | awk '{print $1}')"
run_object "$installed_binary" summary --since all --json
before_events="$(json_get "$object_json" event_count)"
(( before_events >= 1 )) || fail 'synthetic token fixture did not create an event before upgrade'

printf '%s\n' 'Scenario 4: upgrade to the new identity'
run_jsonl "$new_binary" install --yes --json # install --yes --json
upgrade_terminal="$json_terminal"
assert_receipt_paths "$upgrade_terminal"
[[ "$(json_get "$upgrade_terminal" result.identity.version)" == 2.3.6 ]] || fail 'upgrade identity mismatch'
[[ "$(sha256sum "$installed_binary" | awk '{print $1}')" == "$(sha256sum "$new_binary" | awk '{print $1}')" ]] || fail 'upgrade digest mismatch'
[[ "$(sha256sum "$config_path" | awk '{print $1}')" == "$config_sha256" && -f "$database_path" ]] || fail 'upgrade did not preserve config/database'
run_object "$installed_binary" summary --since all --json
after_events="$(json_get "$object_json" event_count)"
(( after_events >= before_events )) || fail 'upgrade lost synthetic database events'
service_mode="$(json_get "$upgrade_terminal" result.service_mode)"
assert_doctor_healthy "$installed_binary" 2.3.6

printf '%s\n' 'Scenario 5: JSON Lines scan'
run_jsonl "$installed_binary" scan --json # scan --json
python3 - "$json_events_file" <<'PY'
import json
import sys
with open(sys.argv[1], encoding='utf-8') as stream:
    progress = [json.loads(line) for line in stream if line.strip()]
if not any(event.get('event') == 'progress' and event.get('phase') == 'scan' for event in progress):
    raise SystemExit('scan emitted no progress event')
PY

printf '%s\n' 'Scenario 6: default uninstall preserves data'
run_jsonl "$installed_binary" uninstall --yes --json # uninstall --yes --json
default_uninstall_terminal="$json_terminal"
assert_receipt_paths "$default_uninstall_terminal"
[[ "$(json_get "$default_uninstall_terminal" result.removal_scheduled)" == false ]] || fail 'Linux uninstall unexpectedly scheduled removal'
[[ "$(json_get "$default_uninstall_terminal" result.program_removed)" == true ]] || fail 'Linux uninstall did not report synchronous removal'
[[ "$(json_get "$default_uninstall_terminal" result.data_preserved)" == true ]] || fail 'Linux default uninstall did not preserve data'
[[ "$(json_get "$default_uninstall_terminal" result.purged)" == false ]] || fail 'Linux default uninstall reported purge'
[[ ! -e "$installed_binary" && -f "$config_path" && -f "$database_path" ]] || fail 'Linux default uninstall path/data semantics failed'
[[ ! -e "$unit_path" ]] || fail 'Linux default uninstall left the user unit'

printf '%s\n' 'Scenario 7-8: synchronous state, reinstall, then purge'
wait_path_absent "$installed_binary"
run_jsonl "$new_binary" install --yes --json # install --yes --json
reinstall_terminal="$json_terminal"
assert_receipt_paths "$reinstall_terminal"
installed_binary="$(json_get "$reinstall_terminal" result.install_path)"
assert_doctor_healthy "$installed_binary" 2.3.6
run_jsonl "$installed_binary" uninstall --purge --yes --json # uninstall --purge --yes --json
purge_terminal="$json_terminal"
assert_receipt_paths "$purge_terminal"
[[ "$(json_get "$purge_terminal" result.removal_scheduled)" == false ]] || fail 'Linux purge unexpectedly scheduled removal'
[[ "$(json_get "$purge_terminal" result.program_removed)" == true ]] || fail 'Linux purge did not report synchronous program removal'
[[ "$(json_get "$purge_terminal" result.data_preserved)" == false ]] || fail 'Linux purge reported preserved data'
[[ "$(json_get "$purge_terminal" result.purged)" == true ]] || fail 'Linux purge did not report completion'

printf '%s\n' 'Scenario 9: final executable/state deletion'
wait_path_absent "$installed_binary"
wait_path_absent "$state_root"
[[ ! -e "$unit_path" ]] || fail 'Linux purge left the user unit'

printf '%s\n' 'Linux current-user lifecycle completed with stable schema_version/event/phase/status/timestamp and one terminal event per command.'
