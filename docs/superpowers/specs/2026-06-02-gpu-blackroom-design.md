# GPU Blackroom Design

## Goal

Add a "GPU blackroom" feature to Beszel that enforces cross-server GPU quotas for selected Docker container names.

The operator can define rules such as `22228-liuyk: max_gpu: 8`. When Beszel detects that this container name is using more than 8 distinct GPUs across all monitored servers, Beszel automatically stops the most recently started GPU-using instance of that container and starts it again after a cooldown period.

## Scope

This feature applies to Docker containers that are already attributed in NVIDIA GPU consumer data. It does not try to modify Docker or NVIDIA runtime behavior before container start. Enforcement is reactive: Beszel detects overuse from monitoring data, then applies a container action through the agent.

The first version supports one enforcement action:

- `stop_then_start`: run `docker stop <container>`, wait for the configured cooldown, then run `docker start <container>`.

## Configuration

The Hub reads a YAML configuration file. The default path should be configurable by environment variable, for example:

```yaml
containers:
  22228-liuyk:
    max_gpu: 8
    action: stop_then_start
    cooldown: 10m
    enabled: true
```

Proposed environment variables:

- `BESZEL_HUB_GPU_BLACKROOM_CONFIG`: path to the YAML config file.
- `BESZEL_HUB_GPU_BLACKROOM_ENABLED`: global enable switch, default `false` for safe rollout.
- `BESZEL_HUB_GPU_BLACKROOM_DEFAULT_COOLDOWN`: default cooldown when a rule omits `cooldown`, default `10m`.

Rules are keyed by exact Docker container name. The initial version does not use wildcard matching, because exact names reduce accidental enforcement.

## Data Source

The agent already returns NVIDIA GPU consumer attribution in:

- `system.CombinedData.Info.GPUSummaries`
- `system.GPUData.Consumers`
- `system.GPUConsumer.Name`
- `system.GPUConsumer.RuntimeSeconds`

The Hub has the current in-memory system data for each monitored server. The Hub should aggregate those summaries after each system update.

## Counting Rule

For each configured container name:

1. Iterate over all online systems.
2. Iterate over each GPU summary for each system.
3. If a GPU has a consumer whose `Name` equals the configured container name, count one usage for that `(systemID, gpuID)` pair.
4. A container using multiple processes on the same GPU still counts as one GPU.
5. A container using multiple GPUs on one server counts once per GPU.
6. A container using GPUs on multiple servers counts across all those servers.

The computed total is compared with `max_gpu`. Enforcement triggers only when `total_gpu_count > max_gpu`.

## Target Selection

When a container exceeds its quota, Beszel should stop the instance that most likely started using GPUs most recently.

Selection rule:

1. Build candidates by grouping usage by `(systemID, containerID/name)`.
2. For each candidate, record the smallest non-zero `RuntimeSeconds` observed across its GPU consumers.
3. Prefer the candidate with the smallest `RuntimeSeconds`.
4. If runtime data is missing for all candidates, fall back to the candidate discovered last in the current aggregation pass.

This preserves older long-running jobs and penalizes the newest over-quota GPU use.

## Enforcement Flow

When overuse is detected:

1. Hub checks whether an enforcement action for the same `(containerName, systemID, containerID)` is already active.
2. Hub checks a short duplicate-action guard to avoid repeated stops from consecutive monitoring updates.
3. Hub calls the selected agent with a new container control action.
4. Agent validates the container ID/name and runs `docker stop`.
5. Hub records enforcement state with:
   - container name
   - system ID and system name
   - container ID
   - observed GPU count
   - limit
   - stop time
   - planned restart time
   - result/error
6. After the cooldown, Hub calls the same agent with `docker start`.
7. Hub updates enforcement state as completed or failed.

The Hub remains the source of truth for cross-server decisions. Agents only execute explicit control requests.

Active cooldowns must be persisted. If the Hub restarts after stopping a container but before restarting it, the Hub must reload active enforcement state on startup and either start the container immediately when the planned restart time has passed or reschedule the remaining cooldown.

## Agent API Changes

Add a new hub-to-agent action, for example `ControlContainer`, with request fields:

- `container_id`
- `operation`: `stop` or `start`

The agent executes Docker API calls over the existing Docker socket. It should only support the required operations and should not expose arbitrary shell commands.

The agent should return a structured result with:

- `operation`
- `container_id`
- `ok`
- `message`

## Safety And Failure Handling

Important guardrails:

- Blackroom enforcement is disabled unless explicitly enabled.
- A rule must have `enabled: true`.
- `max_gpu` must be positive.
- `cooldown` must be at least one minute.
- Stop/start actions are keyed by container ID where available, not only by name.
- The Hub must not schedule multiple restart timers for the same active enforcement.
- If `docker stop` fails, no restart timer is scheduled.
- If `docker start` fails, the failure is logged and exposed in status.
- Active cooldown and restart state must be stored in PocketBase or a small JSON state file and reconciled on Hub startup.

## Observability

The feature should expose enough information to understand what happened:

- Hub logs each over-quota decision and enforcement action.
- A Hub API endpoint returns current blackroom status and recent actions.
- The UI can show a small "GPU blackroom" panel with active cooldowns and recent violations.

Minimum useful status fields:

- container name
- current GPU count
- limit
- selected system
- action state: stopping, cooling_down, restarting, completed, failed
- timestamps
- error message if any

## Testing

Backend tests should cover:

- exact-name rule parsing
- invalid config handling
- counting one GPU once even with multiple processes
- counting across multiple systems
- no action when equal to the limit
- action when greater than the limit
- newest-candidate selection by shortest `RuntimeSeconds`
- duplicate-action suppression
- stop failure does not schedule restart

Agent tests should cover:

- invalid container ID/name rejection
- unsupported operation rejection
- Docker stop/start success path with a test HTTP Docker API server
- Docker stop/start error propagation

## Deployment

This feature requires updating both Hub and agent images because the Hub makes the enforcement decision and the agent executes Docker control operations.

The agent container must have Docker socket access with write capability. A read-only Docker socket mount will not be enough for `docker stop` and `docker start`.

For current Compose deployments, this means changing:

```yaml
volumes:
  - /var/run/docker.sock:/var/run/docker.sock
```

instead of:

```yaml
volumes:
  - /var/run/docker.sock:/var/run/docker.sock:ro
```

This is a significant permission increase. It should only be enabled on servers where automatic enforcement is intended.
