#!/usr/bin/env bash
set -Eeuo pipefail

QUEUE_ROOT="${QUEUE_ROOT:-/home/luhaotao/data/docker_tmp}"
POLL_INTERVAL="${POLL_INTERVAL:-5}"
DEFAULT_SHM_SIZE="${DEFAULT_SHM_SIZE:-1500G}"
DEFAULT_MAX_IMAGE_SIZE_GB="${DEFAULT_MAX_IMAGE_SIZE_GB:-200}"
SSH_CONNECT_TIMEOUT="${SSH_CONNECT_TIMEOUT:-15}"
SSH_STRICT_HOST_KEY_CHECKING="${SSH_STRICT_HOST_KEY_CHECKING:-accept-new}"
SSH_KEY_PATH="${SSH_KEY_PATH:-}"
SSH_CONFIG_PATH="${SSH_CONFIG_PATH:-${HOME}/.ssh/config}"

TASKS_PENDING_DIR="${QUEUE_ROOT}/tasks/pending"
TASKS_RUNNING_DIR="${QUEUE_ROOT}/tasks/running"
TASKS_DONE_DIR="${QUEUE_ROOT}/tasks/done"
TASKS_FAILED_DIR="${QUEUE_ROOT}/tasks/failed"
LOGS_DIR="${QUEUE_ROOT}/logs"
CACHE_DIR="${QUEUE_ROOT}/cache"
STATE_DIR="${QUEUE_ROOT}/state"
WORKER_LOCK_DIR="${STATE_DIR}/worker.lock"

CURRENT_LOG_FILE=""
CURRENT_TASK_JSON=""
CURRENT_STAGE="初始化"
FAIL_REASON=""
SOURCE_ARTIFACTS_CREATED=0
DEST_ARTIFACTS_CREATED=0
LOCAL_CACHE_READY=0

SSH_BASE_ARGS=()
RSYNC_RSH=""

usage() {
	cat <<'EOF'
用法:
  test/test_docker_transfer.sh worker
  test/test_docker_transfer.sh run <task.json>
  test/test_docker_transfer.sh enqueue <task.json>
  test/test_docker_transfer.sh sample
  test/test_docker_transfer.sh help

默认模式:
  不带参数时等同于 worker

环境变量:
  QUEUE_ROOT=/home/luhaotao/data/docker_tmp
  POLL_INTERVAL=5
  SSH_KEY_PATH=/home/luhaotao/.ssh/id_ed25519
  SSH_CONFIG_PATH=/home/luhaotao/.ssh/config
  SSH_STRICT_HOST_KEY_CHECKING=accept-new
  SSH_CONNECT_TIMEOUT=15
  DEFAULT_SHM_SIZE=1500G
  DEFAULT_MAX_IMAGE_SIZE_GB=200

任务目录:
  pending/  等待执行
  running/  正在执行
  done/     执行成功
  failed/   执行失败
  logs/     每个任务一份日志
  cache/    中转缓存

说明:
  1. 这个脚本运行在中转机上，使用中转机本地 SSH 私钥连接源服务器和目标服务器
  2. 后续网页只需要把 JSON 任务文件写入 pending/，worker 就会自动处理
  3. 远端源服务器和目标服务器都需要有 docker 和 skopeo
EOF
}

timestamp() {
	date "+%Y-%m-%d %H:%M:%S"
}

iso_timestamp() {
	date -Iseconds
}

log_line() {
	local level="$1"
	shift
	local line="[$(timestamp)] [$level] $*"
	printf "%s\n" "$line"
	if [[ -n "${CURRENT_LOG_FILE}" ]]; then
		printf "%s\n" "$line" >>"${CURRENT_LOG_FILE}"
	fi
}

log() {
	log_line "INFO" "$*"
}

warn() {
	log_line "WARN" "$*"
}

error() {
	log_line "ERROR" "$*"
}

update_task_progress_file() {
	local json_path="$1"
	local state="$2"
	local stage="$3"
	local message="$4"
	local detail="${5:-}"
	local level="${6:-info}"

	[[ -n "${json_path}" && -f "${json_path}" ]] || return 0

	python3 - "${json_path}" "${state}" "${stage}" "${message}" "${detail}" "${level}" <<'PY'
import json
import os
import sys
from datetime import datetime, timezone

json_path, state, stage, message, detail, level = sys.argv[1:]

if not os.path.exists(json_path):
    raise SystemExit(0)

with open(json_path, "r", encoding="utf-8") as f:
    data = json.load(f)

progress = data.get("progress")
if not isinstance(progress, dict):
    progress = {}

history = progress.get("history")
if not isinstance(history, list):
    history = []

now = datetime.now(timezone.utc).isoformat()

progress["state"] = state
progress["stage"] = stage
progress["message"] = message
progress["updated_at"] = now
if detail:
    progress["detail"] = detail
else:
    progress.pop("detail", None)

history.append(
    {
        "at": now,
        "level": level,
        "state": state,
        "stage": stage,
        "message": message,
    }
)
progress["history"] = history[-40:]
data["progress"] = progress

tmp_path = json_path + ".tmp"
with open(tmp_path, "w", encoding="utf-8") as f:
    json.dump(data, f, ensure_ascii=False, indent=2)
    f.write("\n")
os.replace(tmp_path, json_path)
PY
}

set_task_progress() {
	local state="$1"
	local stage="$2"
	local message="$3"
	local detail="${4:-}"
	local level="${5:-info}"

	update_task_progress_file "${CURRENT_TASK_JSON}" "${state}" "${stage}" "${message}" "${detail}" "${level}"
}

sanitize_segment() {
	local value="${1:-}"
	value="${value//[^a-zA-Z0-9._-]/_}"
	value="${value##_}"
	value="${value%%_}"
	if [[ -z "${value}" ]]; then
		value="task"
	fi
	printf "%s" "${value}"
}

ensure_dirs() {
	mkdir -p \
		"${TASKS_PENDING_DIR}" \
		"${TASKS_RUNNING_DIR}" \
		"${TASKS_DONE_DIR}" \
		"${TASKS_FAILED_DIR}" \
		"${LOGS_DIR}" \
		"${CACHE_DIR}" \
		"${STATE_DIR}"
}

require_cmd() {
	local cmd="$1"
	command -v "${cmd}" >/dev/null 2>&1 || {
		printf "缺少命令: %s\n" "${cmd}" >&2
		exit 1
	}
}

init_transport() {
	SSH_BASE_ARGS=(
		-o "BatchMode=yes"
		-o "ConnectTimeout=${SSH_CONNECT_TIMEOUT}"
		-o "ServerAliveInterval=30"
		-o "ServerAliveCountMax=3"
	)

	if [[ -n "${SSH_STRICT_HOST_KEY_CHECKING}" ]]; then
		SSH_BASE_ARGS+=(-o "StrictHostKeyChecking=${SSH_STRICT_HOST_KEY_CHECKING}")
	fi

	if [[ -n "${SSH_KEY_PATH}" ]]; then
		SSH_BASE_ARGS+=(-i "${SSH_KEY_PATH}")
	fi

	local rsync_ssh=(ssh "${SSH_BASE_ARGS[@]}")
	printf -v RSYNC_RSH "%q " "${rsync_ssh[@]}"
	RSYNC_RSH="${RSYNC_RSH% }"
}

list_ssh_config_aliases() {
	local config_path="$1"

	python3 - "${config_path}" <<'PY'
import glob
import os
import sys

root_path = os.path.expanduser(sys.argv[1])
seen_files = set()
aliases = []

def visit(path):
    expanded = os.path.expanduser(path)
    for match in sorted(glob.glob(expanded)):
        parse_file(os.path.abspath(match))

def parse_file(path):
    if path in seen_files or not os.path.isfile(path):
        return
    seen_files.add(path)
    base_dir = os.path.dirname(path)
    with open(path, "r", encoding="utf-8", errors="replace") as f:
        for raw_line in f:
            line = raw_line.split("#", 1)[0].strip()
            if not line:
                continue
            parts = line.split(None, 1)
            if len(parts) != 2:
                continue
            key, value = parts[0].lower(), parts[1].strip()
            if key == "include":
                for pattern in value.split():
                    if not os.path.isabs(pattern):
                        pattern = os.path.join(base_dir, os.path.expanduser(pattern))
                    visit(pattern)
            elif key == "host":
                for alias in value.split():
                    if alias.startswith("!"):
                        continue
                    if any(ch in alias for ch in "*?[]"):
                        continue
                    aliases.append(alias)

parse_file(os.path.abspath(root_path))

seen_aliases = set()
for alias in aliases:
    if alias in seen_aliases:
        continue
    seen_aliases.add(alias)
    print(alias)
PY
}

find_ssh_alias_for_host() {
	local target_host="$1"
	local config_path="$2"
	local alias=""
	local resolved_host=""

	[[ -n "${target_host}" && -f "${config_path}" ]] || return 1

	while IFS= read -r alias; do
		[[ -n "${alias}" ]] || continue
		resolved_host="$(ssh -F "${config_path}" -G "${alias}" 2>/dev/null | awk '$1 == "hostname" { print $2; exit }')"
		if [[ "${resolved_host}" == "${target_host}" ]]; then
			printf "%s\n" "${alias}"
			return 0
		fi
	done < <(list_ssh_config_aliases "${config_path}")

	return 1
}

resolve_node_via_jump_host() {
	local node="$1"
	local label="$2"
	local config_path="$3"
	local user_part="${node%@*}"
	local host_part="${node#*@}"
	local alias=""

	if [[ "${node}" != *"@"* ]]; then
		error "${label} 缺少 user@host 形式: ${node}" >&2
		return 1
	fi

	if [[ ! -f "${config_path}" ]]; then
		error "已启用跳板机，但未找到 SSH 配置文件: ${config_path}" >&2
		return 1
	fi

	if list_ssh_config_aliases "${config_path}" | grep -Fxq "${host_part}"; then
		log "${label} 直接使用 SSH 配置别名: ${host_part}" >&2
		printf "%s\n" "${node}"
		return 0
	fi

	if alias="$(find_ssh_alias_for_host "${host_part}" "${config_path}")"; then
		log "${label} 使用 SSH 配置别名: ${alias} (HostName ${host_part})" >&2
		printf "%s@%s\n" "${user_part}" "${alias}"
		return 0
	fi

	error "已启用跳板机，但在 ${config_path} 里没有找到 HostName 为 ${host_part} 的 Host 别名" >&2
	return 1
}

apply_jump_host_if_needed() {
	if [[ "${USE_JUMP_HOST:-0}" != "1" ]]; then
		return 0
	fi

	local resolved_source=""
	local resolved_dest=""

	resolved_source="$(resolve_node_via_jump_host "${SOURCE_NODE}" "源服务器" "${SSH_CONFIG_PATH}")" || return 1
	resolved_dest="$(resolve_node_via_jump_host "${DEST_NODE}" "目标服务器" "${SSH_CONFIG_PATH}")" || return 1

	SOURCE_NODE="${resolved_source}"
	DEST_NODE="${resolved_dest}"
}

run_cmd() {
	if [[ -n "${CURRENT_LOG_FILE}" ]]; then
		"$@" 2>&1 | tee -a "${CURRENT_LOG_FILE}"
		return "${PIPESTATUS[0]}"
	fi

	"$@"
}

ssh_remote() {
	local host="$1"
	shift
	run_cmd ssh "${SSH_BASE_ARGS[@]}" "${host}" "$@"
}

quote_for_remote_shell() {
	local value="${1-}"
	printf "'%s'" "${value//\'/\'\"\'\"\'}"
}

build_remote_bash_cmd() {
	local remote_cmd="bash -s --"
	local arg=""
	for arg in "$@"; do
		remote_cmd+=" $(quote_for_remote_shell "${arg}")"
	done
	printf "%s" "${remote_cmd}"
}

ssh_remote_bash() {
	local host="$1"
	shift

	run_cmd ssh "${SSH_BASE_ARGS[@]}" "${host}" "$(build_remote_bash_cmd "$@")"
}

ssh_remote_bash_with_secret() {
	local host="$1"
	local secret="$2"
	shift 2

	local remote_cmd="sh -c $(quote_for_remote_shell 'IFS= read -r BESZEL_REMOTE_SECRET; export BESZEL_REMOTE_SECRET; exec bash -s -- "$@"') _"
	local arg=""
	for arg in "$@"; do
		remote_cmd+=" $(quote_for_remote_shell "${arg}")"
	done

	if [[ -n "${CURRENT_LOG_FILE}" ]]; then
		{ printf "%s\n" "${secret}"; cat; } | ssh "${SSH_BASE_ARGS[@]}" "${host}" "${remote_cmd}" 2>&1 | tee -a "${CURRENT_LOG_FILE}"
		return "${PIPESTATUS[1]}"
	fi

	{ printf "%s\n" "${secret}"; cat; } | ssh "${SSH_BASE_ARGS[@]}" "${host}" "${remote_cmd}"
	return "${PIPESTATUS[1]}"
}

parse_shell_words_assignment() {
	local raw="${1-}"

	python3 - "${raw}" <<'PY'
import shlex
import sys

raw = sys.argv[1]
try:
    tokens = shlex.split(raw)
except ValueError as exc:
    print(f"额外 docker run 参数语法错误: {exc}", file=sys.stderr)
    raise SystemExit(1)

print("parsed_extra_run_args=(" + " ".join(shlex.quote(token) for token in tokens) + ")")
PY
}

rsync_remote() {
	run_cmd rsync -ah --info=progress2 --no-o --no-g --no-perms -O -e "${RSYNC_RSH}" "$@"
}

unique_json_path() {
	local dir="$1"
	local stem="$2"
	local candidate="${dir}/${stem}.json"
	local index=1

	while [[ -e "${candidate}" ]]; do
		candidate="${dir}/${stem}_${index}.json"
		((index++))
	done

	printf "%s\n" "${candidate}"
}

scrub_task_json_file() {
	local json_path="$1"
	python3 - "${json_path}" <<'PY'
import json
import os
import sys

path = sys.argv[1]
if not os.path.exists(path):
    raise SystemExit(0)

with open(path, "r", encoding="utf-8") as f:
    data = json.load(f)

for key in (
    "source_sudo_password",
    "dest_sudo_password",
    "sourceSudoPassword",
    "destSudoPassword",
):
    data.pop(key, None)

with open(path, "w", encoding="utf-8") as f:
    json.dump(data, f, ensure_ascii=False, indent=2)
    f.write("\n")
PY
}

write_result_json() {
	local input_json="$1"
	local output_json="$2"
	local status="$3"
	local message="$4"
	local task_id="$5"
	local source_node="$6"
	local dest_node="$7"
	local container_name="$8"
	local target_image="$9"
	local target_container="${10}"
	local log_file="${11}"
	local started_at="${12}"
	local finished_at="${13}"
	local local_cache_dir="${14}"
	local final_stage="${15}"

	python3 - "${input_json}" "${output_json}" "${status}" "${message}" "${task_id}" "${source_node}" "${dest_node}" "${container_name}" "${target_image}" "${target_container}" "${log_file}" "${started_at}" "${finished_at}" "${local_cache_dir}" "${final_stage}" <<'PY'
import json
import os
import socket
import sys

(
    input_json,
    output_json,
    status,
    message,
    task_id,
    source_node,
    dest_node,
    container_name,
    target_image,
    target_container,
    log_file,
    started_at,
    finished_at,
    local_cache_dir,
    final_stage,
) = sys.argv[1:]

with open(input_json, "r", encoding="utf-8") as f:
    data = json.load(f)

for key in (
    "source_sudo_password",
    "dest_sudo_password",
    "sourceSudoPassword",
    "destSudoPassword",
):
    data.pop(key, None)

progress = data.get("progress")
if not isinstance(progress, dict):
    progress = {}

history = progress.get("history")
if not isinstance(history, list):
    history = []

final_state = "done" if status == "done" else "failed"
final_stage = final_stage or ("任务完成" if status == "done" else "任务失败")

history.append(
    {
        "at": finished_at,
        "level": "info" if status == "done" else "error",
        "state": final_state,
        "stage": final_stage,
        "message": message,
    }
)

progress["state"] = final_state
progress["stage"] = final_stage
progress["message"] = message
progress["updated_at"] = finished_at
progress["history"] = history[-40:]
if status != "done":
    progress["detail"] = message
else:
    progress.pop("detail", None)

data.setdefault("id", task_id)
data["progress"] = progress
data["result"] = {
    "status": status,
    "message": message,
    "relay_host": socket.gethostname(),
    "log_file": log_file,
    "started_at": started_at,
    "finished_at": finished_at,
    "source_node": source_node,
    "dest_node": dest_node,
    "container_name": container_name,
    "target_image": target_image,
    "target_container": target_container,
    "local_cache_dir": local_cache_dir,
}

with open(output_json, "w", encoding="utf-8") as f:
    json.dump(data, f, ensure_ascii=False, indent=2)
    f.write("\n")
PY
}

write_invalid_task_artifacts() {
	local running_json="$1"
	local task_stem="$2"
	local message="$3"
	local target_json

	target_json="$(unique_json_path "${TASKS_FAILED_DIR}" "${task_stem}")"
	mv "${running_json}" "${target_json}"
	CURRENT_TASK_JSON="${target_json}"
	set_task_progress "failed" "${CURRENT_STAGE}" "${message}" "${message}" "error" || true
	scrub_task_json_file "${target_json}" || true
	printf "%s\n" "${message}" >"${target_json%.json}.error.txt"
	error "${message}"
	log "已移入失败队列: ${target_json}"
}

load_task_exports() {
	local task_json="$1"

	eval "$(
		python3 - "${task_json}" <<'PY'
import json
import pathlib
import shlex
import sys

path = sys.argv[1]
with open(path, "r", encoding="utf-8") as f:
    data = json.load(f)

if not isinstance(data, dict):
    raise SystemExit("任务 JSON 顶层必须是对象")

def pick(*names, default=""):
    for name in names:
        if name in data and data[name] is not None:
            return data[name]
    return default

def as_bool(value):
    if isinstance(value, bool):
        return "1" if value else "0"
    if isinstance(value, (int, float)):
        return "1" if value else "0"
    if isinstance(value, str):
        return "1" if value.strip().lower() in {"1", "true", "yes", "y", "on"} else "0"
    return "0"

task_id = pick("id", default=pathlib.Path(path).stem)
target_container = pick("target_container", "custom_name", default="")
mount_bindings = pick("mount_bindings", default=[])

normalized_mount_bindings = []
if isinstance(mount_bindings, list):
    for binding in mount_bindings:
        if not isinstance(binding, dict):
            normalized_mount_bindings.append({"inner_path": "", "dest_vol_dir": ""})
            continue
        normalized_mount_bindings.append({
            "inner_path": str(binding.get("inner_path", binding.get("innerPath", ""))),
            "dest_vol_dir": str(binding.get("dest_vol_dir", binding.get("destVolDir", ""))),
        })

legacy_inner_path = str(pick("inner_path", default=""))
legacy_dest_vol_dir = str(pick("dest_vol_dir", "dest_volume_dir", "target_volume_dir", default=""))
if not normalized_mount_bindings and (legacy_inner_path or legacy_dest_vol_dir):
    normalized_mount_bindings.append({
        "inner_path": legacy_inner_path,
        "dest_vol_dir": legacy_dest_vol_dir,
    })

fields = {
    "TASK_ID": str(task_id),
    "SOURCE_NODE": str(pick("source_node", "source", "src_node")),
    "DEST_NODE": str(pick("dest_node", "target_node", "destination_node")),
    "USE_JUMP_HOST": as_bool(pick("use_jump_host", "useJumpHost", default=False)),
    "CONTAINER_NAME": str(pick("container_name", "source_container", "container")),
    "TARGET_IMAGE": str(pick("target_image", "image")),
    "ALLOW_TARGET_IMAGE_OVERWRITE": as_bool(pick("allow_target_image_overwrite", default=False)),
    "SETUP_VOLUME": as_bool(pick("setup_volume", default=False)),
    "INNER_PATH": legacy_inner_path,
    "DEST_VOL_DIR": legacy_dest_vol_dir,
    "AUTO_START": as_bool(pick("auto_start", default=False)),
    "PORT": str(pick("port", default="")),
    "TARGET_CONTAINER": str(target_container),
    "EXTRA_RUN_ARGS": str(pick("extra_run_args", default="")),
    "SOURCE_SUDO_PASSWORD": str(pick("source_sudo_password", "sourceSudoPassword", default="")),
    "DEST_SUDO_PASSWORD": str(pick("dest_sudo_password", "destSudoPassword", default="")),
    "REMOVE_EXISTING_CONTAINER": as_bool(pick("remove_existing_container", default=False)),
    "KEEP_LOCAL_CACHE": as_bool(pick("keep_local_cache", default=False)),
    "SHM_SIZE": str(pick("shm_size", default="1500G")),
    "MAX_IMAGE_SIZE_GB": str(pick("max_image_size_gb", default="200")),
}

for key, value in fields.items():
    print(f"declare -g {key}={shlex.quote(value)}")

mount_bindings_flat = []
for binding in normalized_mount_bindings:
    mount_bindings_flat.extend([binding["dest_vol_dir"], binding["inner_path"]])

print("declare -ga MOUNT_BINDINGS_FLAT=(" + " ".join(shlex.quote(value) for value in mount_bindings_flat) + ")")
PY
	)"
}

validate_task() {
	local ok=1

	if [[ -z "${SOURCE_NODE}" ]]; then
		error "缺少 source_node"
		ok=0
	fi
	if [[ -z "${DEST_NODE}" ]]; then
		error "缺少 dest_node"
		ok=0
	fi
	if [[ -z "${CONTAINER_NAME}" ]]; then
		error "缺少 container_name"
		ok=0
	fi
	if [[ -z "${TARGET_IMAGE}" ]]; then
		error "缺少 target_image"
		ok=0
	fi
	if [[ -n "${SOURCE_NODE}" && "${SOURCE_NODE}" != *@* ]]; then
		error "source_node 必须写成 user@host，当前值: ${SOURCE_NODE}"
		ok=0
	fi
	if [[ -n "${DEST_NODE}" && "${DEST_NODE}" != *@* ]]; then
		error "dest_node 必须写成 user@host，当前值: ${DEST_NODE}"
		ok=0
	fi

	if [[ "${SETUP_VOLUME}" == "1" ]]; then
		if [[ "${#MOUNT_BINDINGS_FLAT[@]}" -eq 0 ]]; then
			error "setup_volume=true 时至少需要一条挂载目录映射"
			ok=0
		fi
		local i=0
		for ((i = 0; i < ${#MOUNT_BINDINGS_FLAT[@]}; i += 2)); do
			local dest_vol_dir="${MOUNT_BINDINGS_FLAT[i]}"
			local inner_path="${MOUNT_BINDINGS_FLAT[i + 1]:-}"
			if [[ -z "${inner_path}" ]]; then
				error "第 $((i / 2 + 1)) 条挂载缺少 inner_path"
				ok=0
			fi
			if [[ -z "${dest_vol_dir}" ]]; then
				error "第 $((i / 2 + 1)) 条挂载缺少 dest_vol_dir"
				ok=0
			fi
		done
	fi

	if [[ -z "${SHM_SIZE}" ]]; then
		SHM_SIZE="${DEFAULT_SHM_SIZE}"
	fi

	if [[ -n "${PORT}" ]] && [[ ! "${PORT}" =~ ^[0-9]+$ ]]; then
		error "port 必须是数字"
		ok=0
	fi

	if [[ -z "${MAX_IMAGE_SIZE_GB:-}" ]]; then
		MAX_IMAGE_SIZE_GB="${DEFAULT_MAX_IMAGE_SIZE_GB}"
	fi
	if [[ ! "${MAX_IMAGE_SIZE_GB}" =~ ^[0-9]+$ ]] || [[ "${MAX_IMAGE_SIZE_GB}" -le 0 ]]; then
		error "max_image_size_gb 必须是大于 0 的整数"
		ok=0
	fi

	if [[ "${AUTO_START}" == "1" && -n "${EXTRA_RUN_ARGS:-}" ]]; then
		if ! parse_shell_words_assignment "${EXTRA_RUN_ARGS}" >/dev/null; then
			error "额外 docker run 参数语法错误，请检查引号和空格: ${EXTRA_RUN_ARGS}"
			ok=0
		fi
	fi

	if [[ -z "${TARGET_CONTAINER}" ]]; then
		TARGET_CONTAINER="${CONTAINER_NAME}"
	fi

	if [[ "${ok}" != "1" ]]; then
		return 1
	fi
}

claim_one_pending_task() {
	shopt -s nullglob
	local task
	for task in "${TASKS_PENDING_DIR}"/*.json; do
		local name
		name="$(basename "${task}")"
		local claimed="${TASKS_RUNNING_DIR}/${name}"
		if mv "${task}" "${claimed}" 2>/dev/null; then
			printf "%s\n" "${claimed}"
			return 0
		fi
	done

	return 1
}

acquire_worker_lock() {
	if mkdir "${WORKER_LOCK_DIR}" 2>/dev/null; then
		printf "%s\n" "$$" >"${WORKER_LOCK_DIR}/pid"
		trap 'rm -rf "${WORKER_LOCK_DIR}"' EXIT
		return 0
	fi

	local pid="unknown"
	if [[ -f "${WORKER_LOCK_DIR}/pid" ]]; then
		pid="$(cat "${WORKER_LOCK_DIR}/pid" 2>/dev/null || printf "unknown")"
	fi
	printf "已有 worker 正在运行，锁目录: %s (pid=%s)\n" "${WORKER_LOCK_DIR}" "${pid}" >&2
	exit 1
}

verify_remote_sudo_password() {
	local node="$1"
	local secret="$2"
	local label="$3"
	local exit_code="$4"

	if [[ -z "${secret}" ]]; then
		return 0
	fi

	CURRENT_STAGE="校验${label}密码"
	set_task_progress "validating" "${CURRENT_STAGE}" "正在通过 SSH 连接${label}并校验 sudo 权限"
	log "[预检查] 校验${label} sudo 密码"
	ssh_remote_bash_with_secret "${node}" "${secret}" "${label}" "${exit_code}" <<'REMOTE'
set -euo pipefail
label="$1"
exit_code="$2"

command -v sudo >/dev/null 2>&1 || {
	echo "${label}缺少 sudo，无法校验密码" >&2
	exit "${exit_code}"
}

if ! printf '%s\n' "${BESZEL_REMOTE_SECRET}" | sudo -S -k -p '' -v >/dev/null 2>&1; then
	echo "${label} sudo 密码校验失败或当前用户无 sudo 权限" >&2
	exit "${exit_code}"
fi
REMOTE
}

verify_source_sudo_password() {
	verify_remote_sudo_password "${SOURCE_NODE}" "${SOURCE_SUDO_PASSWORD:-}" "源服务器" "104"
}

verify_dest_sudo_password() {
	verify_remote_sudo_password "${DEST_NODE}" "${DEST_SUDO_PASSWORD:-}" "目标服务器" "206"
}

preflight_source() {
	CURRENT_STAGE="检查源服务器"
	set_task_progress "validating" "${CURRENT_STAGE}" "正在检查源服务器依赖和源容器是否存在"
	log "[预检查] 源服务器 ${SOURCE_NODE}"
	ssh_remote_bash "${SOURCE_NODE}" "${CONTAINER_NAME}" <<'REMOTE'
set -euo pipefail
container_name="$1"
command -v docker >/dev/null 2>&1 || { echo "源服务器缺少 docker" >&2; exit 101; }
command -v skopeo >/dev/null 2>&1 || { echo "源服务器缺少 skopeo" >&2; exit 102; }
command -v rsync >/dev/null 2>&1 || { echo "源服务器缺少 rsync" >&2; exit 103; }
docker container inspect "$container_name" >/dev/null 2>&1 || { echo "源容器不存在: $container_name" >&2; exit 105; }
REMOTE
}

preflight_dest() {
	CURRENT_STAGE="检查目标服务器"
	set_task_progress "validating" "${CURRENT_STAGE}" "正在检查目标服务器依赖、镜像冲突和启动条件"
	log "[预检查] 目标服务器 ${DEST_NODE}"
	ssh_remote_bash_with_secret "${DEST_NODE}" "${DEST_SUDO_PASSWORD:-}" "${TARGET_IMAGE}" "${ALLOW_TARGET_IMAGE_OVERWRITE}" "${AUTO_START}" "${TARGET_CONTAINER}" "${REMOVE_EXISTING_CONTAINER}" "${PORT}" "${SETUP_VOLUME}" "${MOUNT_BINDINGS_FLAT[@]}" <<'REMOTE'
set -euo pipefail
target_image="$1"
allow_target_image_overwrite="$2"
auto_start="$3"
target_container="$4"
remove_existing_container="$5"
port="$6"
setup_volume="$7"
shift 7
mount_bindings=("$@")

ensure_dir() {
	local dir_path="$1"

	if mkdir -p "$dir_path" 2>/dev/null; then
		return 0
	fi

	if [[ -n "${BESZEL_REMOTE_SECRET:-}" ]]; then
		if printf '%s\n' "${BESZEL_REMOTE_SECRET}" | sudo -S -p '' mkdir -p "$dir_path" >/dev/null 2>&1; then
			return 0
		fi
	fi

	if sudo -n mkdir -p "$dir_path" >/dev/null 2>&1; then
		return 0
	fi

	echo "创建目标挂载目录失败: $dir_path" >&2
	exit 206
}

command -v docker >/dev/null 2>&1 || { echo "目标服务器缺少 docker" >&2; exit 201; }
command -v skopeo >/dev/null 2>&1 || { echo "目标服务器缺少 skopeo" >&2; exit 202; }
command -v rsync >/dev/null 2>&1 || { echo "目标服务器缺少 rsync" >&2; exit 207; }

if docker image inspect "$target_image" >/dev/null 2>&1 && [[ "$allow_target_image_overwrite" != "1" ]]; then
	echo "目标镜像已存在，且未允许覆盖: $target_image" >&2
	exit 203
fi

if [[ "$auto_start" == "1" ]]; then
	if docker container inspect "$target_container" >/dev/null 2>&1; then
		if [[ "$remove_existing_container" == "1" ]]; then
			docker rm -f "$target_container" >/dev/null
		else
			echo "目标容器已存在，且未允许删除: $target_container" >&2
			exit 204
		fi
	fi

	if [[ -n "$port" ]] && command -v ss >/dev/null 2>&1; then
		if ss -ltn "( sport = :$port )" | tail -n +2 | grep -q .; then
			echo "目标端口已被占用: $port" >&2
			exit 205
		fi
	fi
fi

if [[ "$setup_volume" == "1" ]]; then
	for ((i = 0; i < ${#mount_bindings[@]}; i += 2)); do
		dest_vol_dir="${mount_bindings[i]}"
		ensure_dir "$dest_vol_dir"
	done
fi
REMOTE
}

export_from_source() {
	CURRENT_STAGE="源服务器导出镜像"
	set_task_progress "running" "${CURRENT_STAGE}" "正在从源容器 commit 镜像、校验大小并导出 OCI 目录（当前上限 ${MAX_IMAGE_SIZE_GB} GB）"
	log "[1/5] 源服务器 commit + 导出 OCI 目录"
	ssh_remote_bash "${SOURCE_NODE}" "${CONTAINER_NAME}" "${TEMP_IMAGE_NAME}" "${SOURCE_REMOTE_DIR}" "${TEMP_TAR_SRC}" "${MAX_IMAGE_SIZE_GB}" <<'REMOTE'
set -euo pipefail
container_name="$1"
temp_image_name="$2"
remote_dir="$3"
temp_tar="$4"
max_image_size_gb="$5"

cleanup_failed() {
	rm -rf "$remote_dir"
	rm -f "$temp_tar"
	docker image rm "$temp_image_name:latest" >/dev/null 2>&1 || docker image rm "$temp_image_name" >/dev/null 2>&1 || true
}

trap cleanup_failed ERR

rm -rf "$remote_dir"
mkdir -p "$remote_dir"

docker commit "$container_name" "$temp_image_name" >/dev/null
image_size_bytes="$(docker image inspect "$temp_image_name" --format '{{.Size}}')"
max_image_size_bytes="$(( max_image_size_gb * 1024 * 1024 * 1024 ))"
if (( image_size_bytes > max_image_size_bytes )); then
	actual_size_gb="$(awk -v size="$image_size_bytes" 'BEGIN { printf "%.2f", size / 1024 / 1024 / 1024 }')"
	echo "镜像大小超过限制: ${actual_size_gb} GB > ${max_image_size_gb} GB" >&2
	exit 106
fi
docker save -o "$temp_tar" "$temp_image_name"
skopeo copy "docker-archive:$temp_tar" "dir:$remote_dir"
rm -f "$temp_tar"
trap - ERR
REMOTE
	SOURCE_ARTIFACTS_CREATED=1
}

sync_source_to_local() {
	CURRENT_STAGE="同步源服务器到中转机缓存"
	set_task_progress "running" "${CURRENT_STAGE}" "正在把源服务器导出的镜像同步到中转机缓存"
	log "[2/5] 拉取到中转机缓存 ${LOCAL_CACHE_DIR}"
	rm -rf "${LOCAL_CACHE_DIR}"
	mkdir -p "${LOCAL_CACHE_DIR}"
	LOCAL_CACHE_READY=1
	rsync_remote "${SOURCE_NODE}:${SOURCE_REMOTE_DIR}/" "${LOCAL_CACHE_DIR}/"
}

sync_local_to_dest() {
	CURRENT_STAGE="同步中转机缓存到目标服务器"
	set_task_progress "running" "${CURRENT_STAGE}" "正在把中转机缓存推送到目标服务器"
	log "[3/5] 推送到目标服务器 ${DEST_NODE}"
	ssh_remote_bash_with_secret "${DEST_NODE}" "${DEST_SUDO_PASSWORD:-}" "${DEST_REMOTE_DIR}" "${SETUP_VOLUME}" "${MOUNT_BINDINGS_FLAT[@]}" <<'REMOTE'
set -euo pipefail
remote_dir="$1"
setup_volume="$2"
shift 2
mount_bindings=("$@")

ensure_dir() {
	local dir_path="$1"

	if mkdir -p "$dir_path" 2>/dev/null; then
		return 0
	fi

	if [[ -n "${BESZEL_REMOTE_SECRET:-}" ]]; then
		if printf '%s\n' "${BESZEL_REMOTE_SECRET}" | sudo -S -p '' mkdir -p "$dir_path" >/dev/null 2>&1; then
			return 0
		fi
	fi

	if sudo -n mkdir -p "$dir_path" >/dev/null 2>&1; then
		return 0
	fi

	echo "创建目标目录失败: $dir_path" >&2
	exit 206
}

rm -rf "$remote_dir"
mkdir -p "$remote_dir"
if [[ "$setup_volume" == "1" ]]; then
	for ((i = 0; i < ${#mount_bindings[@]}; i += 2)); do
		dest_vol_dir="${mount_bindings[i]}"
		ensure_dir "$dest_vol_dir"
	done
fi
REMOTE
	DEST_ARTIFACTS_CREATED=1
	rsync_remote "${LOCAL_CACHE_DIR}/" "${DEST_NODE}:${DEST_REMOTE_DIR}/"
}

import_on_dest() {
	CURRENT_STAGE="目标服务器导入镜像"
	set_task_progress "running" "${CURRENT_STAGE}" "正在把 OCI 目录导入目标服务器 Docker"
	log "[4/5] 目标服务器导入 Docker 镜像 ${TARGET_IMAGE}"
	ssh_remote_bash "${DEST_NODE}" "${DEST_REMOTE_DIR}" "${TEMP_TAR_DEST}" "${TARGET_IMAGE}" <<'REMOTE'
set -euo pipefail
remote_dir="$1"
temp_tar="$2"
target_image="$3"
skopeo copy "dir:$remote_dir" "docker-archive:$temp_tar:$target_image"
docker load -i "$temp_tar"
rm -f "$temp_tar"
REMOTE
}

auto_start_on_dest() {
	if [[ "${AUTO_START}" != "1" ]]; then
		return 0
	fi

	local -a parsed_extra_run_args=()
	if [[ -n "${EXTRA_RUN_ARGS:-}" ]]; then
		local parsed_assignment=""
		if ! parsed_assignment="$(parse_shell_words_assignment "${EXTRA_RUN_ARGS}")"; then
			error "额外 docker run 参数解析失败: ${EXTRA_RUN_ARGS}"
			return 1
		fi
		eval "${parsed_assignment}"
	fi

	local mount_arg_count="${#MOUNT_BINDINGS_FLAT[@]}"
	CURRENT_STAGE="目标服务器自动启动容器"
	set_task_progress "running" "${CURRENT_STAGE}" "正在目标服务器执行 docker run 自动启动容器"
	log "[5/5] 自动启动目标容器 ${TARGET_CONTAINER}"
	ssh_remote_bash "${DEST_NODE}" "${TARGET_CONTAINER}" "${TARGET_IMAGE}" "${PORT}" "${SHM_SIZE}" "${mount_arg_count}" "${MOUNT_BINDINGS_FLAT[@]}" "${parsed_extra_run_args[@]}" <<'REMOTE'
set -euo pipefail
target_container="$1"
target_image="$2"
port="$3"
shm_size="$4"
mount_arg_count="$5"
shift 5
args=("$@")
mount_args=()
if (( mount_arg_count > 0 )); then
	mount_args=("${args[@]:0:mount_arg_count}")
	args=("${args[@]:mount_arg_count}")
fi
extra_run_args=("${args[@]}")

run_args=(docker run -d --gpus all --network=host --restart=always --shm-size="$shm_size" --name "$target_container")
for ((i = 0; i < ${#mount_args[@]}; i += 2)); do
	dest_vol_dir="${mount_args[i]}"
	inner_path="${mount_args[i + 1]}"
	run_args+=(-v "$dest_vol_dir:$inner_path")
done
if [[ "${#extra_run_args[@]}" -gt 0 ]]; then
	run_args+=("${extra_run_args[@]}")
fi

if [[ -n "$port" ]]; then
	run_args+=("$target_image" /usr/sbin/sshd -p "$port" -D)
else
	run_args+=("$target_image")
fi

"${run_args[@]}"
REMOTE
}

cleanup_remote_artifacts() {
	local cleanup_failed=0

	if [[ "${SOURCE_ARTIFACTS_CREATED:-0}" == "1" && -n "${SOURCE_NODE:-}" && -n "${SOURCE_REMOTE_DIR:-}" ]]; then
		ssh_remote_bash "${SOURCE_NODE}" "${SOURCE_REMOTE_DIR}" "${TEMP_TAR_SRC:-}" "${TEMP_IMAGE_NAME:-}" <<'REMOTE' || cleanup_failed=1
set -euo pipefail
remote_dir="$1"
temp_tar="$2"
temp_image_name="$3"
rm -rf "$remote_dir"
if [[ -n "$temp_tar" ]]; then
	rm -f "$temp_tar"
fi
if [[ -n "$temp_image_name" ]]; then
	docker image rm "$temp_image_name:latest" >/dev/null 2>&1 || docker image rm "$temp_image_name" >/dev/null 2>&1 || true
fi
REMOTE
	fi

	if [[ "${DEST_ARTIFACTS_CREATED:-0}" == "1" && -n "${DEST_NODE:-}" && -n "${DEST_REMOTE_DIR:-}" ]]; then
		ssh_remote_bash "${DEST_NODE}" "${DEST_REMOTE_DIR}" "${TEMP_TAR_DEST:-}" <<'REMOTE' || cleanup_failed=1
set -euo pipefail
remote_dir="$1"
temp_tar="$2"
rm -rf "$remote_dir"
if [[ -n "$temp_tar" ]]; then
	rm -f "$temp_tar"
fi
REMOTE
	fi

	if [[ "${KEEP_LOCAL_CACHE:-0}" != "1" && "${LOCAL_CACHE_READY:-0}" == "1" && -n "${LOCAL_CACHE_DIR:-}" ]]; then
		rm -rf "${LOCAL_CACHE_DIR}" || cleanup_failed=1
	fi

	return "${cleanup_failed}"
}

run_transfer_pipeline() {
	verify_source_sudo_password || return 1
	verify_dest_sudo_password || return 1
	preflight_source || return 1
	preflight_dest || return 1
	export_from_source || return 1
	sync_source_to_local || return 1
	sync_local_to_dest || return 1
	import_on_dest || return 1
	auto_start_on_dest || return 1
}

process_claimed_task() {
	local running_json="$1"
	local task_name
	task_name="$(basename "${running_json}")"
	local task_stem="${task_name%.json}"
	CURRENT_LOG_FILE="${LOGS_DIR}/${task_stem}.log"
	CURRENT_TASK_JSON="${running_json}"
	: >"${CURRENT_LOG_FILE}"

	local task_started_at
	task_started_at="$(iso_timestamp)"
	CURRENT_STAGE="解析任务参数"
	set_task_progress "running" "${CURRENT_STAGE}" "worker 已领取任务，正在解析和校验参数" || true

	log "开始处理任务: ${running_json}"
	log "日志文件: ${CURRENT_LOG_FILE}"

	if ! load_task_exports "${running_json}"; then
		write_invalid_task_artifacts "${running_json}" "${task_stem}" "任务 JSON 非法，无法解析，请检查格式"
		return 1
	fi

	if ! validate_task; then
		write_invalid_task_artifacts "${running_json}" "${task_stem}" "任务字段不完整或不合法，请检查日志"
		return 1
	fi

	if ! apply_jump_host_if_needed; then
		write_invalid_task_artifacts "${running_json}" "${task_stem}" "跳板机 SSH 配置解析失败，请检查 relay 机 ~/.ssh/config"
		return 1
	fi

	set_task_progress "validating" "本地参数校验" "任务参数校验通过，准备连接源服务器和目标服务器" || true

	local task_id_safe
	task_id_safe="$(sanitize_segment "${TASK_ID}")"

	TEMP_IMAGE_NAME="beszel-transfer-${task_id_safe}"
	SOURCE_REMOTE_DIR="/tmp/beszel-transfer-src-${task_id_safe}"
	DEST_REMOTE_DIR="/tmp/beszel-transfer-dst-${task_id_safe}"
	TEMP_TAR_SRC="/tmp/beszel-transfer-src-${task_id_safe}.tar"
	TEMP_TAR_DEST="/tmp/beszel-transfer-dst-${task_id_safe}.tar"
	LOCAL_CACHE_DIR="${CACHE_DIR}/${task_id_safe}"
	SOURCE_ARTIFACTS_CREATED=0
	DEST_ARTIFACTS_CREATED=0
	LOCAL_CACHE_READY=0

	log "任务 ID: ${TASK_ID}"
	log "源服务器: ${SOURCE_NODE}"
	log "目标服务器: ${DEST_NODE}"
	log "源容器: ${CONTAINER_NAME}"
	log "目标镜像: ${TARGET_IMAGE}"
	log "目标容器: ${TARGET_CONTAINER}"
	log "使用跳板机: ${USE_JUMP_HOST:-0}"
	log "自动启动: ${AUTO_START}"
	if [[ -n "${EXTRA_RUN_ARGS:-}" ]]; then
		log "额外 docker run 参数: ${EXTRA_RUN_ARGS}"
	fi
	log "保留中转缓存: ${KEEP_LOCAL_CACHE}"

	local status="done"
	local message="迁移成功"

	if ! run_transfer_pipeline; then
		status="failed"
		message="迁移失败，最后阶段: ${CURRENT_STAGE}"
		error "${message}"
	fi

	if ! cleanup_remote_artifacts; then
		warn "清理临时文件时有部分步骤失败"
	fi

	local task_finished_at
	task_finished_at="$(iso_timestamp)"

	local result_json
	if [[ "${status}" == "done" ]]; then
		result_json="$(unique_json_path "${TASKS_DONE_DIR}" "${task_stem}")"
	else
		result_json="$(unique_json_path "${TASKS_FAILED_DIR}" "${task_stem}")"
	fi

	write_result_json \
		"${running_json}" \
		"${result_json}" \
		"${status}" \
		"${message}" \
		"${TASK_ID}" \
		"${SOURCE_NODE}" \
		"${DEST_NODE}" \
		"${CONTAINER_NAME}" \
		"${TARGET_IMAGE}" \
		"${TARGET_CONTAINER}" \
		"${CURRENT_LOG_FILE}" \
		"${task_started_at}" \
		"${task_finished_at}" \
		"${LOCAL_CACHE_DIR}" \
		"${CURRENT_STAGE}"

	rm -f "${running_json}"
	CURRENT_TASK_JSON=""

	if [[ "${status}" == "done" ]]; then
		log "任务完成: ${result_json}"
		return 0
	fi

	log "任务失败: ${result_json}"
	return 1
}

worker_loop() {
	acquire_worker_lock
	log "worker 已启动"
	log "队列根目录: ${QUEUE_ROOT}"
	log "轮询间隔: ${POLL_INTERVAL}s"

	while true; do
		local claimed_task=""
		if claimed_task="$(claim_one_pending_task)"; then
			process_claimed_task "${claimed_task}" || true
			continue
		fi

		sleep "${POLL_INTERVAL}"
	done
}

enqueue_task() {
	local source_json="$1"
	[[ -f "${source_json}" ]] || {
		printf "任务文件不存在: %s\n" "${source_json}" >&2
		exit 1
	}

	python3 - "${source_json}" >/dev/null <<'PY'
import json
import sys
with open(sys.argv[1], "r", encoding="utf-8") as f:
    data = json.load(f)
if not isinstance(data, dict):
    raise SystemExit("任务 JSON 顶层必须是对象")
PY

	local stem
	stem="$(sanitize_segment "$(basename "${source_json}" .json)")"
	local target_json
	target_json="$(unique_json_path "${TASKS_PENDING_DIR}" "${stem}")"
	cp "${source_json}" "${target_json}"
	printf "已入队: %s\n" "${target_json}"
}

run_single_task() {
	local source_json="$1"
	[[ -f "${source_json}" ]] || {
		printf "任务文件不存在: %s\n" "${source_json}" >&2
		exit 1
	}

	local stem
	stem="$(sanitize_segment "$(basename "${source_json}" .json)")"
	local running_json
	running_json="$(unique_json_path "${TASKS_RUNNING_DIR}" "${stem}")"
	cp "${source_json}" "${running_json}"
	process_claimed_task "${running_json}"
}

print_sample() {
	cat <<'EOF'
{
  "id": "demo-transfer-001",
  "source_node": "xsuper@10.15.88.84",
  "dest_node": "xsuper@10.15.88.85",
  "use_jump_host": false,
  "container_name": "rlinf-v1.1-qianyx",
  "target_image": "rlinf:v1.1-migrated",
  "allow_target_image_overwrite": false,
  "setup_volume": true,
  "mount_bindings": [
    {
      "inner_path": "/data",
      "dest_vol_dir": "/data/containers/demo"
    }
  ],
  "inner_path": "",
  "dest_vol_dir": "",
  "auto_start": false,
  "port": "",
  "target_container": "",
  "extra_run_args": "",
  "source_sudo_password": "",
  "dest_sudo_password": "",
  "remove_existing_container": false,
  "keep_local_cache": false,
  "shm_size": "1500G",
  "max_image_size_gb": 200
}
EOF
}

main() {
	require_cmd bash
	require_cmd python3
	require_cmd ssh
	require_cmd rsync
	require_cmd tee

	local mode="${1:-worker}"
	case "${mode}" in
		worker)
			ensure_dirs
			init_transport
			worker_loop
			;;
		run)
			[[ $# -ge 2 ]] || {
				usage
				exit 1
			}
			ensure_dirs
			init_transport
			run_single_task "$2"
			;;
		enqueue)
			[[ $# -ge 2 ]] || {
				usage
				exit 1
			}
			ensure_dirs
			init_transport
			enqueue_task "$2"
			;;
		sample)
			print_sample
			;;
		help|-h|--help)
			usage
			;;
		*)
			usage
			exit 1
			;;
	esac
}

main "$@"
