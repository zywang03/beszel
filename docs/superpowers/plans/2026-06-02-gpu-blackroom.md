# GPU Blackroom Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Enforce cross-server GPU quotas for configured Docker container names by stopping the newest over-quota GPU-using container and restarting it after a cooldown.

**Architecture:** The Hub reads a YAML quota config, aggregates existing agent GPU consumer summaries across all active systems, selects the newest over-quota candidate, and calls the selected agent with a new container control RPC. The agent only exposes limited Docker `stop` and `start` operations, while the Hub persists active cooldown state in a JSON file under the Hub data directory so restarts survive Hub process restarts.

**Tech Stack:** Go 1.26, PocketBase app lifecycle, Beszel hub/agent CBOR RPC, Docker HTTP API, `gopkg.in/yaml.v3`, Go tests with `testify`.

---

### File Structure

- Modify `internal/common/common-ws.go`: add `ControlContainer`, request type, and response type.
- Modify `agent/docker.go`: add Docker control helper for `stop` and `start`.
- Modify `agent/handlers.go`: register and implement `ControlContainerHandler`.
- Modify `agent/docker_test.go`: test Docker control endpoint validation and Docker API calls.
- Modify `agent/handlers_test.go`: test handler registration and behavior.
- Create `internal/hub/gpu_blackroom.go`: config parsing, aggregation, candidate selection, enforcement state, JSON persistence, and restart scheduling.
- Create `internal/hub/gpu_blackroom_test.go`: test config parsing, GPU counting, candidate selection, duplicate suppression, and persistence.
- Modify `internal/hub/hub.go`: add `gpuBlackroom` manager field and initialize/start it.
- Modify `internal/hub/systems/system.go`: call blackroom evaluation after successful record creation.
- Modify `internal/hub/api.go`: expose `/api/beszel/gpu-blackroom/status`.
- Modify `internal/hub/transport/transport.go`: teach legacy response unmarshalling about `ControlContainer`.

### Task 1: Agent Docker Control RPC

**Files:**
- Modify: `internal/common/common-ws.go`
- Modify: `agent/docker.go`
- Modify: `agent/handlers.go`
- Modify: `internal/hub/transport/transport.go`
- Test: `agent/docker_test.go`
- Test: `agent/handlers_test.go`

- [ ] **Step 1: Write failing Docker control tests**

Add tests to `agent/docker_test.go`:

```go
func TestControlContainerUsesExpectedDockerEndpoint(t *testing.T) {
	tests := []struct {
		name      string
		operation string
		path      string
	}{
		{name: "stop", operation: "stop", path: "/containers/0123456789ab/stop"},
		{name: "start", operation: "start", path: "/containers/0123456789ab/start"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var method string
			var path string
			dm := &dockerManager{
				client: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
					method = req.Method
					path = req.URL.EscapedPath()
					return &http.Response{
						StatusCode: http.StatusNoContent,
						Status:     "204 No Content",
						Header:     make(http.Header),
						Body:       io.NopCloser(strings.NewReader("")),
						Request:    req,
					}, nil
				})},
			}

			result, err := dm.controlContainer(context.Background(), "0123456789ab", tt.operation)

			require.NoError(t, err)
			assert.True(t, result.Ok)
			assert.Equal(t, tt.operation, result.Operation)
			assert.Equal(t, "0123456789ab", result.ContainerID)
			assert.Equal(t, http.MethodPost, method)
			assert.Equal(t, tt.path, path)
		})
	}
}

func TestControlContainerRejectsInvalidInput(t *testing.T) {
	dm := &dockerManager{client: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		t.Fatal("Docker API should not be called for invalid input")
		return nil, nil
	})}}

	_, err := dm.controlContainer(context.Background(), "../version", "stop")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid container id")

	_, err = dm.controlContainer(context.Background(), "0123456789ab", "remove")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported container operation")
}
```

- [ ] **Step 2: Run agent Docker tests and verify failure**

Run:

```bash
go test -tags testing ./agent -run 'TestControlContainer' -count=1
```

Expected: FAIL because `controlContainer` does not exist.

- [ ] **Step 3: Add common RPC types**

In `internal/common/common-ws.go`, add `ControlContainer` after `GetSystemdInfo` and add:

```go
type ContainerControlRequest struct {
	ContainerID string `cbor:"0,keyasint"`
	Operation   string `cbor:"1,keyasint"`
}

type ContainerControlResponse struct {
	Operation   string `cbor:"0,keyasint"`
	ContainerID string `cbor:"1,keyasint"`
	Ok          bool   `cbor:"2,keyasint"`
	Message     string `cbor:"3,keyasint,omitempty,omitzero"`
}
```

- [ ] **Step 4: Implement Docker control helper**

In `agent/docker.go`, add:

```go
func normalizeContainerControlOperation(operation string) (string, error) {
	switch strings.TrimSpace(strings.ToLower(operation)) {
	case "stop":
		return "stop", nil
	case "start":
		return "start", nil
	default:
		return "", fmt.Errorf("unsupported container operation: %s", operation)
	}
}

func (dm *dockerManager) controlContainer(ctx context.Context, containerID string, operation string) (common.ContainerControlResponse, error) {
	operation, err := normalizeContainerControlOperation(operation)
	if err != nil {
		return common.ContainerControlResponse{}, err
	}
	endpoint, err := buildDockerContainerEndpoint(containerID, operation, nil)
	if err != nil {
		return common.ContainerControlResponse{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
	if err != nil {
		return common.ContainerControlResponse{}, err
	}
	resp, err := dm.client.Do(req)
	if err != nil {
		return common.ContainerControlResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusNotModified {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		msg := strings.TrimSpace(string(body))
		if msg == "" {
			msg = resp.Status
		}
		return common.ContainerControlResponse{}, fmt.Errorf("docker %s failed for container %s: %s", operation, containerID, msg)
	}
	return common.ContainerControlResponse{
		Operation:   operation,
		ContainerID: containerID,
		Ok:          true,
		Message:     resp.Status,
	}, nil
}
```

Add `github.com/henrygd/beszel/internal/common` to imports.

- [ ] **Step 5: Register and implement agent handler**

In `agent/handlers.go`, register `common.ControlContainer` and add:

```go
type ControlContainerHandler struct{}

func (h *ControlContainerHandler) Handle(hctx *HandlerContext) error {
	if hctx.Agent.dockerManager == nil {
		return errors.New("docker manager is unavailable")
	}
	var req common.ContainerControlRequest
	if err := cbor.Unmarshal(hctx.Request.Data, &req); err != nil {
		return err
	}
	ctx := context.Background()
	result, err := hctx.Agent.dockerManager.controlContainer(ctx, req.ContainerID, req.Operation)
	if err != nil {
		return err
	}
	return hctx.SendResponse(result, hctx.RequestID)
}
```

- [ ] **Step 6: Update transport legacy switch**

In `internal/hub/transport/transport.go`, add a `ControlContainer` case that unmarshals from generic `Data` only and returns a clear error if a legacy agent lacks generic response data.

- [ ] **Step 7: Add handler tests**

Add to `agent/handlers_test.go`:

```go
func TestControlContainerHandlerRegistered(t *testing.T) {
	registry := NewHandlerRegistry()
	handler, exists := registry.GetHandler(common.ControlContainer)
	assert.True(t, exists)
	assert.IsType(t, &ControlContainerHandler{}, handler)
}
```

- [ ] **Step 8: Run agent tests**

Run:

```bash
go test -tags testing ./agent -run 'TestControlContainer|TestHandlerRegistry' -count=1
```

Expected: PASS.

### Task 2: Hub Blackroom Core Logic

**Files:**
- Create: `internal/hub/gpu_blackroom.go`
- Test: `internal/hub/gpu_blackroom_test.go`

- [ ] **Step 1: Write failing core tests**

Create `internal/hub/gpu_blackroom_test.go` with tests for:

```go
func TestLoadGPUBlackroomConfig(t *testing.T)
func TestGPUBlackroomCountsDistinctGPUsAcrossSystems(t *testing.T)
func TestGPUBlackroomSelectsShortestRuntimeCandidate(t *testing.T)
func TestGPUBlackroomDoesNotTriggerWhenAtLimit(t *testing.T)
func TestGPUBlackroomSuppressesDuplicateActiveEnforcement(t *testing.T)
```

Use helper data with `system.CombinedData{Info: system.Info{GPUSummaries: map[string]system.GPUData{...}}}` and consumers named `22228-liuyk`.

- [ ] **Step 2: Run hub blackroom tests and verify failure**

Run:

```bash
go test -tags testing ./internal/hub -run 'TestGPUBlackroom|TestLoadGPUBlackroom' -count=1
```

Expected: FAIL because blackroom types/functions do not exist.

- [ ] **Step 3: Implement config parsing**

Create `internal/hub/gpu_blackroom.go` with:

- `gpuBlackroomConfig`
- `gpuBlackroomRule`
- `loadGPUBlackroomConfig(path string, globalEnabled bool, defaultCooldown time.Duration) (gpuBlackroomConfig, error)`
- validation for enabled rules, positive `max_gpu`, and cooldown at least one minute.

- [ ] **Step 4: Implement aggregation and candidate selection**

Add:

- `gpuBlackroomSnapshot`
- `gpuBlackroomCandidate`
- `collectGPUBlackroomSnapshot(rules map[string]gpuBlackroomRule, systems map[string]*systems.System) gpuBlackroomSnapshot`
- `selectGPUBlackroomCandidate(snapshot, rule)`.

Count one `(systemID, gpuID)` once per container name.

- [ ] **Step 5: Implement in-memory active state**

Add `gpuBlackroomManager` with mutex-protected `active map[string]gpuBlackroomEnforcement` and duplicate suppression by `(containerName, systemID, containerID)`.

- [ ] **Step 6: Run core tests**

Run:

```bash
go test -tags testing ./internal/hub -run 'TestGPUBlackroom|TestLoadGPUBlackroom' -count=1
```

Expected: PASS.

### Task 3: Hub Enforcement, Persistence, and Startup Recovery

**Files:**
- Modify: `internal/hub/gpu_blackroom.go`
- Modify: `internal/hub/hub.go`
- Modify: `internal/hub/systems/system.go`
- Test: `internal/hub/gpu_blackroom_test.go`
- Test helper: `internal/hub/hub_test_helpers.go` if needed

- [ ] **Step 1: Write failing persistence and scheduling tests**

Add tests:

```go
func TestGPUBlackroomPersistsActiveEnforcement(t *testing.T)
func TestGPUBlackroomLoadsActiveEnforcement(t *testing.T)
func TestGPUBlackroomSchedulesRestartAfterStop(t *testing.T)
```

Use a fake control function on the manager instead of real agent calls.

- [ ] **Step 2: Run tests and verify failure**

Run:

```bash
go test -tags testing ./internal/hub -run 'TestGPUBlackroom.*Enforcement|TestGPUBlackroom.*Restart' -count=1
```

Expected: FAIL because persistence/scheduling is not implemented.

- [ ] **Step 3: Implement state file persistence**

Persist active enforcements to:

```go
filepath.Join(h.DataDir(), "gpu_blackroom_state.json")
```

Implement atomic write using temp file + rename.

- [ ] **Step 4: Implement `EvaluateSystemGPUBlackroom` path**

Add a Hub method:

```go
func (h *Hub) EvaluateGPUBlackroom() {
	if h.gpuBlackroom == nil {
		return
	}
	h.gpuBlackroom.Evaluate()
}
```

Call it after successful `sys.createRecords(data)` in `internal/hub/systems/system.go` through a new method in the `hubLike` interface.

- [ ] **Step 5: Implement agent control call from Hub**

Add `ControlContainerFromAgent(containerID, operation string)` to `internal/hub/systems/system.go`, using `sys.request(ctx, common.ControlContainer, common.ContainerControlRequest{...}, &result)`.

- [ ] **Step 6: Implement stop/start scheduling**

When over quota:

1. Call selected system with `stop`.
2. Persist active state with planned restart time.
3. Start a timer for cooldown.
4. Timer calls `start`.
5. Remove active state on successful start, or mark failed on error.

On Hub startup, reload state and reschedule pending starts.

- [ ] **Step 7: Wire Hub initialization**

In `NewHub`, create `gpuBlackroom` manager. In `StartHub` after system manager initialization, start/recover blackroom timers.

- [ ] **Step 8: Run hub tests**

Run:

```bash
go test -tags testing ./internal/hub ./internal/hub/systems -run 'TestGPUBlackroom|TestCombinedData' -count=1
```

Expected: PASS.

### Task 4: Status API

**Files:**
- Modify: `internal/hub/api.go`
- Modify: `internal/hub/gpu_blackroom.go`
- Test: `internal/hub/api_test.go` or `internal/hub/gpu_blackroom_test.go`

- [ ] **Step 1: Write failing API test**

Add a route test for:

```text
GET /api/beszel/gpu-blackroom/status
```

Expected JSON includes:

- `enabled`
- `rules`
- `active`
- `recent`

- [ ] **Step 2: Run API test and verify failure**

Run:

```bash
go test -tags testing ./internal/hub -run 'TestGPUBlackroomStatus' -count=1
```

Expected: FAIL because route does not exist.

- [ ] **Step 3: Implement status route**

Register `apiAuth.GET("/gpu-blackroom/status", h.getGPUBlackroomStatus)` and return `h.gpuBlackroom.Status()`.

- [ ] **Step 4: Run API test**

Run:

```bash
go test -tags testing ./internal/hub -run 'TestGPUBlackroomStatus' -count=1
```

Expected: PASS.

### Task 5: Full Verification

**Files:**
- All modified files

- [ ] **Step 1: Run targeted tests**

Run:

```bash
go test -tags testing ./agent ./internal/hub ./internal/hub/systems -count=1
```

Expected: PASS.

- [ ] **Step 2: Run formatting**

Run:

```bash
gofmt -w internal/common/common-ws.go agent/docker.go agent/handlers.go internal/hub/transport/transport.go internal/hub/gpu_blackroom.go internal/hub/gpu_blackroom_test.go internal/hub/hub.go internal/hub/systems/system.go internal/hub/api.go agent/docker_test.go agent/handlers_test.go
```

- [ ] **Step 3: Run broader Go tests if time permits**

Run:

```bash
go test -tags testing ./internal/hub/... ./agent -count=1
```

Expected: PASS.

- [ ] **Step 4: Check git diff**

Run:

```bash
git diff --stat
git diff --check
```

Expected: no whitespace errors.
