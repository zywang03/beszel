package hub

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/henrygd/beszel"
	"github.com/henrygd/beszel/internal/common"
	"github.com/pocketbase/pocketbase/core"
	"golang.org/x/crypto/ssh"
)

const defaultDockerTransferQueueRoot = "/home/luhaotao/data/docker_tmp"
const defaultDockerTransferMaxImageSizeGB int64 = 200

var dockerTransferSegmentSanitizer = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

type dockerTransferMountBindingRequest struct {
	InnerPath  string `json:"innerPath"`
	DestVolDir string `json:"destVolDir"`
}

type dockerTransferMountBindingFile struct {
	InnerPath  string `json:"inner_path"`
	DestVolDir string `json:"dest_vol_dir"`
}

type dockerTransferTaskProgressEntry struct {
	At      string `json:"at"`
	Level   string `json:"level,omitempty"`
	State   string `json:"state,omitempty"`
	Stage   string `json:"stage,omitempty"`
	Message string `json:"message,omitempty"`
}

type dockerTransferTaskProgress struct {
	State     string                            `json:"state"`
	Stage     string                            `json:"stage,omitempty"`
	Message   string                            `json:"message,omitempty"`
	Detail    string                            `json:"detail,omitempty"`
	UpdatedAt string                            `json:"updated_at,omitempty"`
	History   []dockerTransferTaskProgressEntry `json:"history,omitempty"`
}

type dockerTransferTaskRequest struct {
	SourceSystemID            string                              `json:"sourceSystemId"`
	SourceSystemName          string                              `json:"sourceSystemName"`
	DestSystemID              string                              `json:"destSystemId"`
	DestSystemName            string                              `json:"destSystemName"`
	SourceUser                string                              `json:"sourceUser"`
	SourceNode                string                              `json:"sourceNode"`
	DestUser                  string                              `json:"destUser"`
	DestNode                  string                              `json:"destNode"`
	UseJumpHost               bool                                `json:"useJumpHost"`
	ContainerName             string                              `json:"containerName"`
	TargetImage               string                              `json:"targetImage"`
	AllowTargetImageOverwrite bool                                `json:"allowTargetImageOverwrite"`
	SetupVolume               bool                                `json:"setupVolume"`
	MountBindings             []dockerTransferMountBindingRequest `json:"mountBindings"`
	InnerPath                 string                              `json:"innerPath"`
	DestVolDir                string                              `json:"destVolDir"`
	AutoStart                 bool                                `json:"autoStart"`
	Port                      string                              `json:"port"`
	TargetContainer           string                              `json:"targetContainer"`
	ExtraRunArgs              string                              `json:"extraRunArgs"`
	SourceSudoPassword        string                              `json:"sourceSudoPassword"`
	DestSudoPassword          string                              `json:"destSudoPassword"`
	RemoveExistingContainer   bool                                `json:"removeExistingContainer"`
	KeepLocalCache            bool                                `json:"keepLocalCache"`
	ShmSize                   string                              `json:"shmSize"`
}

type dockerTransferTaskFile struct {
	ID                        string                           `json:"id"`
	RequestedBy               string                           `json:"requested_by,omitempty"`
	RequestedAt               string                           `json:"requested_at"`
	SourceSystemID            string                           `json:"source_system_id,omitempty"`
	SourceSystemName          string                           `json:"source_system_name,omitempty"`
	DestSystemID              string                           `json:"dest_system_id,omitempty"`
	DestSystemName            string                           `json:"dest_system_name,omitempty"`
	SourceNode                string                           `json:"source_node"`
	DestNode                  string                           `json:"dest_node"`
	UseJumpHost               bool                             `json:"use_jump_host"`
	ContainerName             string                           `json:"container_name"`
	TargetImage               string                           `json:"target_image"`
	AllowTargetImageOverwrite bool                             `json:"allow_target_image_overwrite"`
	SetupVolume               bool                             `json:"setup_volume"`
	MountBindings             []dockerTransferMountBindingFile `json:"mount_bindings,omitempty"`
	InnerPath                 string                           `json:"inner_path,omitempty"`
	DestVolDir                string                           `json:"dest_vol_dir,omitempty"`
	AutoStart                 bool                             `json:"auto_start"`
	Port                      string                           `json:"port,omitempty"`
	TargetContainer           string                           `json:"target_container,omitempty"`
	ExtraRunArgs              string                           `json:"extra_run_args,omitempty"`
	SourceSudoPassword        string                           `json:"source_sudo_password,omitempty"`
	DestSudoPassword          string                           `json:"dest_sudo_password,omitempty"`
	RemoveExistingContainer   bool                             `json:"remove_existing_container"`
	KeepLocalCache            bool                             `json:"keep_local_cache"`
	ShmSize                   string                           `json:"shm_size"`
	MaxImageSizeGB            int64                            `json:"max_image_size_gb,omitempty"`
	Progress                  *dockerTransferTaskProgress      `json:"progress,omitempty"`
}

func requireAdminRole(e *core.RequestEvent) error {
	if e.Auth == nil || e.Auth.GetString("role") != "admin" {
		return e.ForbiddenError("Requires admin role", nil)
	}
	return nil
}

func getDockerTransferQueueRoot() string {
	if queueRoot, exists := GetEnv("DOCKER_TRANSFER_QUEUE_ROOT"); exists && strings.TrimSpace(queueRoot) != "" {
		return strings.TrimSpace(queueRoot)
	}
	if relayDir, exists := GetEnv("DOCKER_TRANSFER_RELAY_DIR"); exists && strings.TrimSpace(relayDir) != "" {
		return strings.TrimSpace(relayDir)
	}
	return defaultDockerTransferQueueRoot
}

func getDockerTransferRelayUser() string {
	if relayUser, exists := GetEnv("DOCKER_TRANSFER_RELAY_USER"); exists && strings.TrimSpace(relayUser) != "" {
		return strings.TrimSpace(relayUser)
	}
	return ""
}

func getDockerTransferRelayHost() string {
	if relayHost, exists := GetEnv("DOCKER_TRANSFER_RELAY_HOST"); exists && strings.TrimSpace(relayHost) != "" {
		return strings.TrimSpace(relayHost)
	}
	hostname, err := os.Hostname()
	if err != nil {
		return ""
	}
	return hostname
}

func getDockerTransferRelayPort() string {
	if relayPort, exists := GetEnv("DOCKER_TRANSFER_RELAY_PORT"); exists && strings.TrimSpace(relayPort) != "" {
		return strings.TrimSpace(relayPort)
	}
	return "22"
}

func getDockerTransferMaxImageSizeGB() int64 {
	if value, exists := GetEnv("DOCKER_TRANSFER_MAX_IMAGE_SIZE_GB"); exists && strings.TrimSpace(value) != "" {
		parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		if err == nil && parsed > 0 {
			return parsed
		}
	}
	return defaultDockerTransferMaxImageSizeGB
}

func useLocalDockerTransferWrite() bool {
	if localWrite, exists := GetEnv("DOCKER_TRANSFER_LOCAL_WRITE"); exists {
		localWrite = strings.TrimSpace(strings.ToLower(localWrite))
		return localWrite == "1" || localWrite == "true" || localWrite == "yes"
	}
	return false
}

func resolveDockerTransferRelayTarget() (user string, host string) {
	user = getDockerTransferRelayUser()
	host = getDockerTransferRelayHost()

	if strings.Contains(host, "@") {
		parts := strings.SplitN(host, "@", 2)
		if user == "" {
			user = strings.TrimSpace(parts[0])
		}
		host = strings.TrimSpace(parts[1])
	}

	return strings.TrimSpace(user), strings.TrimSpace(host)
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

func sanitizeDockerTransferSegment(value string) string {
	value = strings.TrimSpace(value)
	value = dockerTransferSegmentSanitizer.ReplaceAllString(value, "_")
	value = strings.Trim(value, "._-")
	if value == "" {
		return "task"
	}
	return value
}

func isExplicitSSHNode(value string) bool {
	value = strings.TrimSpace(value)
	return strings.Contains(value, "@")
}

func normalizeSSHNode(user string, node string) (string, error) {
	user = strings.TrimSpace(user)
	node = strings.TrimSpace(node)

	if node == "" {
		return "", fmt.Errorf("host is required")
	}

	if isExplicitSSHNode(node) {
		parts := strings.SplitN(node, "@", 2)
		if user == "" {
			user = strings.TrimSpace(parts[0])
		}
		node = strings.TrimSpace(parts[1])
	}

	if user == "" || node == "" {
		return "", fmt.Errorf("ssh target must include both user and host")
	}

	return fmt.Sprintf("%s@%s", user, node), nil
}

func normalizeDockerTransferMountBindings(
	reqBindings []dockerTransferMountBindingRequest,
	fallbackInner string,
	fallbackDest string,
) []dockerTransferMountBindingFile {
	mountBindings := make([]dockerTransferMountBindingFile, 0, len(reqBindings))
	for _, binding := range reqBindings {
		mountBindings = append(mountBindings, dockerTransferMountBindingFile{
			InnerPath:  strings.TrimSpace(binding.InnerPath),
			DestVolDir: strings.TrimSpace(binding.DestVolDir),
		})
	}

	if len(mountBindings) == 0 && (strings.TrimSpace(fallbackInner) != "" || strings.TrimSpace(fallbackDest) != "") {
		mountBindings = append(mountBindings, dockerTransferMountBindingFile{
			InnerPath:  strings.TrimSpace(fallbackInner),
			DestVolDir: strings.TrimSpace(fallbackDest),
		})
	}

	return mountBindings
}

func hasConflictingAutoStartArgs(port string, extraRunArgs string) bool {
	if strings.TrimSpace(port) == "" || strings.TrimSpace(extraRunArgs) == "" {
		return false
	}

	lowerArgs := strings.ToLower(extraRunArgs)
	return strings.Contains(lowerArgs, "--entrypoint") || strings.Contains(lowerArgs, "sshd")
}

func (h *Hub) createDockerTransferRelaySSHConfig() (*ssh.ClientConfig, error) {
	privateKey, err := h.GetSSHKey("")
	if err != nil {
		return nil, err
	}

	return &ssh.ClientConfig{
		User: "",
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(privateKey),
		},
		Config: ssh.Config{
			Ciphers:      common.DefaultCiphers,
			KeyExchanges: common.DefaultKeyExchanges,
			MACs:         common.DefaultMACs,
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		ClientVersion:   fmt.Sprintf("SSH-2.0-%s_%s", beszel.AppName, beszel.Version),
		Timeout:         15 * time.Second,
	}, nil
}

func writeDockerTransferTaskLocally(jsonData []byte, pendingDir string, queueFile string, tmpFile string) error {
	if err := os.MkdirAll(pendingDir, 0755); err != nil {
		return err
	}
	if err := os.WriteFile(tmpFile, jsonData, 0644); err != nil {
		return err
	}
	if err := os.Rename(tmpFile, queueFile); err != nil {
		_ = os.Remove(tmpFile)
		return err
	}
	return nil
}

func newDockerTransferTaskProgress(state string, stage string, message string) *dockerTransferTaskProgress {
	now := time.Now().UTC().Format(time.RFC3339)
	return &dockerTransferTaskProgress{
		State:     state,
		Stage:     stage,
		Message:   message,
		UpdatedAt: now,
		History: []dockerTransferTaskProgressEntry{
			{
				At:      now,
				Level:   "info",
				State:   state,
				Stage:   stage,
				Message: message,
			},
		},
	}
}

func scrubDockerTransferSecrets(value any) {
	switch typed := value.(type) {
	case map[string]any:
		delete(typed, "source_sudo_password")
		delete(typed, "dest_sudo_password")
		delete(typed, "sourceSudoPassword")
		delete(typed, "destSudoPassword")
		for _, child := range typed {
			scrubDockerTransferSecrets(child)
		}
	case []any:
		for _, child := range typed {
			scrubDockerTransferSecrets(child)
		}
	}
}

func tailDockerTransferLogLines(path string, limit int) []string {
	if limit <= 0 {
		limit = 30
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	normalized := strings.ReplaceAll(string(data), "\r\n", "\n")
	lines := strings.Split(normalized, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) > limit {
		lines = lines[len(lines)-limit:]
	}
	return lines
}

func defaultDockerTransferProgress(queueState string) map[string]any {
	stage := "等待执行"
	message := "任务已提交，等待 worker 领取"
	state := queueState

	switch queueState {
	case "running":
		stage = "执行中"
		message = "worker 已领取任务，正在执行"
	case "done":
		stage = "任务完成"
		message = "迁移任务已完成"
	case "failed":
		stage = "任务失败"
		message = "迁移任务执行失败"
	}

	return map[string]any{
		"state":      state,
		"stage":      stage,
		"message":    message,
		"updated_at": time.Now().UTC().Format(time.RFC3339),
	}
}

func normalizeDockerTransferStatusMountBindings(task map[string]any) []map[string]string {
	mountBindings := make([]map[string]string, 0)

	if rawBindings, ok := task["mount_bindings"].([]any); ok {
		for _, rawBinding := range rawBindings {
			binding, ok := rawBinding.(map[string]any)
			if !ok {
				continue
			}

			innerPath, _ := binding["inner_path"].(string)
			if strings.TrimSpace(innerPath) == "" {
				innerPath, _ = binding["innerPath"].(string)
			}

			destVolDir, _ := binding["dest_vol_dir"].(string)
			if strings.TrimSpace(destVolDir) == "" {
				destVolDir, _ = binding["destVolDir"].(string)
			}

			innerPath = strings.TrimSpace(innerPath)
			destVolDir = strings.TrimSpace(destVolDir)
			if innerPath == "" && destVolDir == "" {
				continue
			}

			mountBindings = append(mountBindings, map[string]string{
				"innerPath":  innerPath,
				"destVolDir": destVolDir,
			})
		}
	}

	if len(mountBindings) == 0 {
		innerPath, _ := task["inner_path"].(string)
		destVolDir, _ := task["dest_vol_dir"].(string)
		innerPath = strings.TrimSpace(innerPath)
		destVolDir = strings.TrimSpace(destVolDir)
		if innerPath != "" || destVolDir != "" {
			mountBindings = append(mountBindings, map[string]string{
				"innerPath":  innerPath,
				"destVolDir": destVolDir,
			})
		}
	}

	return mountBindings
}

func buildDockerTransferTaskStatusResponse(queueRoot string, taskID string, queueState string, task map[string]any) map[string]any {
	scrubDockerTransferSecrets(task)

	progress, ok := task["progress"].(map[string]any)
	if !ok || len(progress) == 0 {
		progress = defaultDockerTransferProgress(queueState)
	}

	var result map[string]any
	if parsedResult, ok := task["result"].(map[string]any); ok {
		result = parsedResult
	}

	logFile := filepath.ToSlash(filepath.Join(queueRoot, "logs", taskID+".log"))
	if result != nil {
		if resultLogFile, ok := result["log_file"].(string); ok && strings.TrimSpace(resultLogFile) != "" {
			logFile = strings.TrimSpace(resultLogFile)
		}
	}

	mountBindings := normalizeDockerTransferStatusMountBindings(task)
	setupVolume, _ := task["setup_volume"].(bool)
	if !setupVolume && len(mountBindings) > 0 {
		setupVolume = true
	}
	autoStart, _ := task["auto_start"].(bool)
	port, _ := task["port"].(string)

	return map[string]any{
		"found":           true,
		"taskId":          taskID,
		"queueState":      queueState,
		"sourceNode":      task["source_node"],
		"destNode":        task["dest_node"],
		"containerName":   task["container_name"],
		"targetImage":     task["target_image"],
		"targetContainer": task["target_container"],
		"setupVolume":     setupVolume,
		"mountBindings":   mountBindings,
		"autoStart":       autoStart,
		"port":            strings.TrimSpace(port),
		"progress":        progress,
		"result":          result,
		"logTail":         tailDockerTransferLogLines(logFile, 30),
	}
}

func readDockerTransferTaskStatusLocally(taskID string) (map[string]any, error) {
	queueRoot := getDockerTransferQueueRoot()
	for _, queueState := range []string{"done", "failed", "running", "pending"} {
		taskPath := filepath.Join(queueRoot, "tasks", queueState, taskID+".json")
		data, err := os.ReadFile(taskPath)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}

		task := map[string]any{}
		if err := json.Unmarshal(data, &task); err != nil {
			return nil, err
		}

		return buildDockerTransferTaskStatusResponse(queueRoot, taskID, queueState, task), nil
	}

	return map[string]any{
		"found":      false,
		"taskId":     taskID,
		"queueState": "missing",
		"logTail":    []string{},
	}, nil
}

func (h *Hub) writeDockerTransferTaskToRelay(jsonData []byte, pendingDir string, queueFile string, tmpFile string) error {
	if useLocalDockerTransferWrite() {
		return writeDockerTransferTaskLocally(jsonData, pendingDir, queueFile, tmpFile)
	}

	relayUser, relayHost := resolveDockerTransferRelayTarget()
	if relayUser == "" || relayHost == "" {
		return fmt.Errorf("docker transfer relay target is incomplete: user=%q host=%q", relayUser, relayHost)
	}

	sshConfig, err := h.createDockerTransferRelaySSHConfig()
	if err != nil {
		return fmt.Errorf("failed to prepare relay ssh config: %w", err)
	}
	sshConfig.User = relayUser

	client, err := ssh.Dial("tcp", net.JoinHostPort(relayHost, getDockerTransferRelayPort()), sshConfig)
	if err != nil {
		return fmt.Errorf("failed to connect relay %s@%s: %w", relayUser, relayHost, err)
	}
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("failed to create relay ssh session: %w", err)
	}
	defer session.Close()

	session.Stdin = bytes.NewReader(jsonData)
	var stderr bytes.Buffer
	session.Stderr = &stderr

	cmd := fmt.Sprintf(
		"mkdir -p %s && cat > %s && mv %s %s",
		shellQuote(pendingDir),
		shellQuote(tmpFile),
		shellQuote(tmpFile),
		shellQuote(queueFile),
	)
	if err := session.Run(cmd); err != nil {
		if stderr.Len() > 0 {
			return fmt.Errorf("failed to upload task to relay: %w: %s", err, strings.TrimSpace(stderr.String()))
		}
		return fmt.Errorf("failed to upload task to relay: %w", err)
	}

	return nil
}

func (h *Hub) readDockerTransferTaskStatusFromRelay(taskID string) (map[string]any, error) {
	if useLocalDockerTransferWrite() {
		return readDockerTransferTaskStatusLocally(taskID)
	}

	relayUser, relayHost := resolveDockerTransferRelayTarget()
	if relayUser == "" || relayHost == "" {
		return nil, fmt.Errorf("docker transfer relay target is incomplete: user=%q host=%q", relayUser, relayHost)
	}

	sshConfig, err := h.createDockerTransferRelaySSHConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to prepare relay ssh config: %w", err)
	}
	sshConfig.User = relayUser

	client, err := ssh.Dial("tcp", net.JoinHostPort(relayHost, getDockerTransferRelayPort()), sshConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to connect relay %s@%s: %w", relayUser, relayHost, err)
	}
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		return nil, fmt.Errorf("failed to create relay ssh session: %w", err)
	}
	defer session.Close()

	remoteScript := `
import json
import os
import sys

queue_root = sys.argv[1]
task_id = sys.argv[2]
secret_keys = {
    "source_sudo_password",
    "dest_sudo_password",
    "sourceSudoPassword",
    "destSudoPassword",
}

def scrub(obj):
    if isinstance(obj, dict):
        for key in list(obj.keys()):
            if key in secret_keys:
                obj.pop(key, None)
                continue
            scrub(obj[key])
    elif isinstance(obj, list):
        for item in obj:
            scrub(item)

def tail_lines(path, limit=30):
    if not path or not os.path.exists(path):
        return []
    with open(path, "r", encoding="utf-8", errors="replace") as f:
        lines = [line.rstrip("\n") for line in f.readlines()]
    return lines[-limit:]

def default_progress(queue_state):
    stage = "等待执行"
    message = "任务已提交，等待 worker 领取"
    if queue_state == "running":
        stage = "执行中"
        message = "worker 已领取任务，正在执行"
    elif queue_state == "done":
        stage = "任务完成"
        message = "迁移任务已完成"
    elif queue_state == "failed":
        stage = "任务失败"
        message = "迁移任务执行失败"
    return {
        "state": queue_state,
        "stage": stage,
        "message": message,
    }

def normalize_mount_bindings(task):
    result = []
    raw_bindings = task.get("mount_bindings")
    if isinstance(raw_bindings, list):
        for item in raw_bindings:
            if not isinstance(item, dict):
                continue
            inner_path = str(item.get("inner_path") or item.get("innerPath") or "").strip()
            dest_vol_dir = str(item.get("dest_vol_dir") or item.get("destVolDir") or "").strip()
            if not inner_path and not dest_vol_dir:
                continue
            result.append({
                "innerPath": inner_path,
                "destVolDir": dest_vol_dir,
            })

    if not result:
        inner_path = str(task.get("inner_path") or "").strip()
        dest_vol_dir = str(task.get("dest_vol_dir") or "").strip()
        if inner_path or dest_vol_dir:
            result.append({
                "innerPath": inner_path,
                "destVolDir": dest_vol_dir,
            })

    return result

response = {
    "found": False,
    "taskId": task_id,
    "queueState": "missing",
    "logTail": [],
}

for queue_state in ("done", "failed", "running", "pending"):
    task_path = os.path.join(queue_root, "tasks", queue_state, task_id + ".json")
    if not os.path.exists(task_path):
        continue

    with open(task_path, "r", encoding="utf-8") as f:
        task = json.load(f)

    scrub(task)
    progress = task.get("progress")
    if not isinstance(progress, dict) or not progress:
        progress = default_progress(queue_state)

    result = task.get("result")
    log_file = os.path.join(queue_root, "logs", task_id + ".log")
    if isinstance(result, dict) and result.get("log_file"):
        log_file = result["log_file"]
    mount_bindings = normalize_mount_bindings(task)
    setup_volume = bool(task.get("setup_volume")) or len(mount_bindings) > 0

    response = {
        "found": True,
        "taskId": task_id,
        "queueState": queue_state,
        "sourceNode": task.get("source_node"),
        "destNode": task.get("dest_node"),
        "containerName": task.get("container_name"),
        "targetImage": task.get("target_image"),
        "targetContainer": task.get("target_container"),
        "setupVolume": setup_volume,
        "mountBindings": mount_bindings,
        "autoStart": bool(task.get("auto_start")),
        "port": str(task.get("port") or ""),
        "progress": progress,
        "result": result if isinstance(result, dict) else None,
        "logTail": tail_lines(log_file, 30),
    }
    break

print(json.dumps(response, ensure_ascii=False))
`

	session.Stdin = bytes.NewBufferString(remoteScript)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr

	cmd := fmt.Sprintf(
		"python3 - %s %s",
		shellQuote(getDockerTransferQueueRoot()),
		shellQuote(taskID),
	)
	if err := session.Run(cmd); err != nil {
		if stderr.Len() > 0 {
			return nil, fmt.Errorf("failed to read relay task status: %w: %s", err, strings.TrimSpace(stderr.String()))
		}
		return nil, fmt.Errorf("failed to read relay task status: %w", err)
	}

	response := map[string]any{}
	if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
		return nil, fmt.Errorf("failed to decode relay task status: %w", err)
	}

	return response, nil
}

func (h *Hub) getDockerTransferConfig(e *core.RequestEvent) error {
	if err := requireAdminRole(e); err != nil {
		return err
	}
	queueRoot := getDockerTransferQueueRoot()
	relayUser, relayHost := resolveDockerTransferRelayTarget()
	return e.JSON(http.StatusOK, map[string]any{
		"enabled":        true,
		"relayHost":      relayHost,
		"relayUser":      relayUser,
		"relayDir":       queueRoot,
		"queueRoot":      queueRoot,
		"maxImageSizeGb": getDockerTransferMaxImageSizeGB(),
	})
}

func (h *Hub) getDockerTransferTaskStatus(e *core.RequestEvent) error {
	if err := requireAdminRole(e); err != nil {
		return err
	}

	taskID := strings.TrimSpace(e.Request.URL.Query().Get("taskId"))
	if taskID == "" {
		return e.BadRequestError("taskId is required", nil)
	}

	status, err := h.readDockerTransferTaskStatusFromRelay(taskID)
	if err != nil {
		return e.InternalServerError("Failed to read docker transfer task status", err)
	}

	return e.JSON(http.StatusOK, status)
}

func (h *Hub) createDockerTransferTask(e *core.RequestEvent) error {
	if err := requireAdminRole(e); err != nil {
		return err
	}

	reqData := dockerTransferTaskRequest{
		ShmSize: "1500G",
	}
	if err := e.BindBody(&reqData); err != nil {
		return e.BadRequestError("Bad data", err)
	}

	reqData.SourceSystemID = strings.TrimSpace(reqData.SourceSystemID)
	reqData.SourceSystemName = strings.TrimSpace(reqData.SourceSystemName)
	reqData.DestSystemID = strings.TrimSpace(reqData.DestSystemID)
	reqData.DestSystemName = strings.TrimSpace(reqData.DestSystemName)
	reqData.SourceUser = strings.TrimSpace(reqData.SourceUser)
	reqData.SourceNode = strings.TrimSpace(reqData.SourceNode)
	reqData.DestUser = strings.TrimSpace(reqData.DestUser)
	reqData.DestNode = strings.TrimSpace(reqData.DestNode)
	reqData.ContainerName = strings.TrimSpace(reqData.ContainerName)
	reqData.TargetImage = strings.TrimSpace(reqData.TargetImage)
	reqData.InnerPath = strings.TrimSpace(reqData.InnerPath)
	reqData.DestVolDir = strings.TrimSpace(reqData.DestVolDir)
	reqData.Port = strings.TrimSpace(reqData.Port)
	reqData.TargetContainer = strings.TrimSpace(reqData.TargetContainer)
	reqData.ExtraRunArgs = strings.TrimSpace(reqData.ExtraRunArgs)
	reqData.SourceSudoPassword = strings.TrimSpace(reqData.SourceSudoPassword)
	reqData.DestSudoPassword = strings.TrimSpace(reqData.DestSudoPassword)
	reqData.ShmSize = strings.TrimSpace(reqData.ShmSize)

	mountBindings := normalizeDockerTransferMountBindings(reqData.MountBindings, reqData.InnerPath, reqData.DestVolDir)

	if reqData.SourceNode == "" || reqData.DestNode == "" || reqData.ContainerName == "" || reqData.TargetImage == "" {
		return e.BadRequestError("sourceNode, destNode, containerName and targetImage are required", nil)
	}
	if reqData.SetupVolume {
		if len(mountBindings) == 0 {
			return e.BadRequestError("at least one mount binding is required when setupVolume is enabled", nil)
		}
		for _, binding := range mountBindings {
			if binding.InnerPath == "" || binding.DestVolDir == "" {
				return e.BadRequestError("each mount binding requires innerPath and destVolDir", nil)
			}
		}
		if len(mountBindings) > 0 {
			reqData.InnerPath = mountBindings[0].InnerPath
			reqData.DestVolDir = mountBindings[0].DestVolDir
		}
	} else {
		mountBindings = nil
		reqData.InnerPath = ""
		reqData.DestVolDir = ""
	}
	if reqData.ShmSize == "" {
		reqData.ShmSize = "1500G"
	}
	if hasConflictingAutoStartArgs(reqData.Port, reqData.ExtraRunArgs) {
		return e.BadRequestError("extraRunArgs cannot contain --entrypoint or sshd when startup port is set", nil)
	}

	sourceSSHNode, err := normalizeSSHNode(reqData.SourceUser, reqData.SourceNode)
	if err != nil {
		return e.BadRequestError("source server ssh user and host are required", err)
	}
	destSSHNode, err := normalizeSSHNode(reqData.DestUser, reqData.DestNode)
	if err != nil {
		return e.BadRequestError("destination server ssh user and host are required", err)
	}

	queueRoot := getDockerTransferQueueRoot()
	pendingDir := filepath.ToSlash(filepath.Join(queueRoot, "tasks", "pending"))

	taskID := fmt.Sprintf(
		"%s-%s-%s",
		time.Now().UTC().Format("20060102-150405"),
		sanitizeDockerTransferSegment(reqData.ContainerName),
		uuid.NewString()[:8],
	)
	queueFile := filepath.ToSlash(filepath.Join(pendingDir, taskID+".json"))
	tmpFile := filepath.ToSlash(filepath.Join(pendingDir, "."+taskID+".tmp"))

	requestedBy := e.Auth.Id
	if email := strings.TrimSpace(e.Auth.GetString("email")); email != "" {
		requestedBy = email
	}

	taskFile := dockerTransferTaskFile{
		ID:                        taskID,
		RequestedBy:               requestedBy,
		RequestedAt:               time.Now().UTC().Format(time.RFC3339),
		SourceSystemID:            reqData.SourceSystemID,
		SourceSystemName:          reqData.SourceSystemName,
		DestSystemID:              reqData.DestSystemID,
		DestSystemName:            reqData.DestSystemName,
		SourceNode:                sourceSSHNode,
		DestNode:                  destSSHNode,
		UseJumpHost:               reqData.UseJumpHost,
		ContainerName:             reqData.ContainerName,
		TargetImage:               reqData.TargetImage,
		AllowTargetImageOverwrite: reqData.AllowTargetImageOverwrite,
		SetupVolume:               reqData.SetupVolume,
		MountBindings:             mountBindings,
		InnerPath:                 reqData.InnerPath,
		DestVolDir:                reqData.DestVolDir,
		AutoStart:                 reqData.AutoStart,
		Port:                      reqData.Port,
		TargetContainer:           reqData.TargetContainer,
		ExtraRunArgs:              reqData.ExtraRunArgs,
		SourceSudoPassword:        reqData.SourceSudoPassword,
		DestSudoPassword:          reqData.DestSudoPassword,
		RemoveExistingContainer:   reqData.RemoveExistingContainer,
		KeepLocalCache:            reqData.KeepLocalCache,
		ShmSize:                   reqData.ShmSize,
		MaxImageSizeGB:            getDockerTransferMaxImageSizeGB(),
		Progress:                  newDockerTransferTaskProgress("pending", "等待执行", "任务已提交，等待 worker 领取"),
	}

	jsonData, err := json.MarshalIndent(taskFile, "", "  ")
	if err != nil {
		return e.InternalServerError("Failed to encode docker transfer task", err)
	}
	jsonData = append(jsonData, '\n')

	if err := h.writeDockerTransferTaskToRelay(jsonData, pendingDir, queueFile, tmpFile); err != nil {
		return e.InternalServerError("Failed to publish docker transfer task", err)
	}

	relayUser, relayHost := resolveDockerTransferRelayTarget()
	return e.JSON(http.StatusOK, map[string]any{
		"success":    true,
		"taskId":     taskID,
		"queueFile":  queueFile,
		"pendingDir": pendingDir,
		"relayHost":  relayHost,
		"relayUser":  relayUser,
		"relayDir":   queueRoot,
	})
}
