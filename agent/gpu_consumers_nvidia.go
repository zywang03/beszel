package agent

import (
	"context"
	"encoding/xml"
	"fmt"
	"log/slog"
	"maps"
	"math"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/henrygd/beszel/agent/utils"
	"github.com/henrygd/beszel/internal/entities/container"
	"github.com/henrygd/beszel/internal/entities/system"
)

const (
	hostProcRoot          = "/host/proc"
	nvidiaSmiConsumerRead = 8 * time.Second
)

var (
	dockerScopePattern = regexp.MustCompile(`docker-([[:xdigit:]]{12,64})\.scope`)
	dockerPathPattern  = regexp.MustCompile(`/docker/([[:xdigit:]]{12,64})(?:/|$)`)
	clockTicksOnce     sync.Once
	clockTicksPerSec   = 100.0
)

var containerCgroupPrefixes = []string{
	"docker-",
	"cri-containerd-",
	"containerd-",
	"crio-",
	"libpod-",
}

type nvidiaSmiLog struct {
	GPUs []nvidiaSmiGPU `xml:"gpu"`
}

type nvidiaSmiGPU struct {
	MinorNumber string             `xml:"minor_number"`
	Processes   nvidiaSmiProcesses `xml:"processes"`
}

type nvidiaSmiProcesses struct {
	ProcessInfo []nvidiaSmiProcess `xml:"process_info"`
}

type nvidiaSmiProcess struct {
	PID        string `xml:"pid"`
	UsedMemory string `xml:"used_memory"`
}

func (a *Agent) updateGpuSummaries(data *system.CombinedData, cacheTimeMs uint16) {
	if cacheTimeMs != 60_000 || len(data.Stats.GPUData) == 0 {
		return
	}

	if consumersByGPU := a.getNvidiaGPUConsumers(data.Stats.GPUData, data.Containers); len(consumersByGPU) > 0 {
		for gpuID, gpu := range data.Stats.GPUData {
			gpu.Consumers = consumersByGPU[gpuID]
			data.Stats.GPUData[gpuID] = gpu
		}
	}

	data.Info.GPUSummaries = cloneGPUDataMap(data.Stats.GPUData)
}

func cloneGPUDataMap(src map[string]system.GPUData) map[string]system.GPUData {
	if len(src) == 0 {
		return nil
	}

	dst := make(map[string]system.GPUData, len(src))
	for id, gpu := range src {
		if gpu.Engines != nil {
			gpu.Engines = maps.Clone(gpu.Engines)
		}
		if gpu.Consumers != nil {
			gpu.Consumers = append([]system.GPUConsumer(nil), gpu.Consumers...)
		}
		dst[id] = gpu
	}
	return dst
}

func (a *Agent) getNvidiaGPUConsumers(gpus map[string]system.GPUData, containers []*container.Stats) map[string][]system.GPUConsumer {
	if len(gpus) == 0 || len(containers) == 0 || a.dockerManager == nil || a.dockerManager.IsPodman() {
		return nil
	}

	processData, err := readNvidiaGPUProcesses()
	if err != nil {
		slog.Debug("Failed to read NVIDIA GPU consumers", "err", err)
		return nil
	}

	totalProcesses := 0
	for _, gpu := range processData.GPUs {
		totalProcesses += len(gpu.Processes.ProcessInfo)
	}
	slog.Debug("NVIDIA GPU process data", "gpus", len(processData.GPUs), "total_processes", totalProcesses)

	containerNames := make(map[string]string, len(containers))
	for _, ctr := range containers {
		if ctr == nil || ctr.Id == "" {
			continue
		}
		containerNames[ctr.Id] = ctr.Name
	}
	if len(containerNames) == 0 {
		return nil
	}
	knownContainerIDs := make(map[string]struct{}, len(containerNames))
	for containerID := range containerNames {
		knownContainerIDs[containerID] = struct{}{}
	}

	procRoot := procRootForConsumers()
	var pidToContainerID map[string]containerPIDAttribution
	uptimeSeconds, hasUptime := readProcUptimeSeconds(procRoot)
	runtimeCache := make(map[string]uint64)
	consumersByGPU := make(map[string]map[string]*system.GPUConsumer, len(processData.GPUs))
	unattributedByGPU := make(map[string]*system.GPUConsumer, len(processData.GPUs))

	for idx, gpu := range processData.GPUs {
		gpuID := resolveNvidiaGPUID(gpu, idx, gpus)
		if _, ok := gpus[gpuID]; !ok {
			slog.Debug("Skipping GPU from NVIDIA process data: ID not in known GPU data", "resolved_id", gpuID, "minor_number", gpu.MinorNumber, "index", idx, "process_count", len(gpu.Processes.ProcessInfo))
			continue
		}

		for _, process := range gpu.Processes.ProcessInfo {
			if process.PID == "" {
				continue
			}

			var (
				containerID    string
				hostPID        string
				runtimeSeconds uint64
				hasRuntime     bool
			)

			if a.dockerManager != nil {
				if pidToContainerID == nil {
					pidToContainerID = a.dockerManager.buildContainerPIDMap(containers)
					slog.Debug("Built container PID map", "pid_count", len(pidToContainerID))
				}
				if attribution, ok := pidToContainerID[process.PID]; ok {
					containerID = attribution.ContainerID
					hostPID = attribution.HostPID
				}
			}
			if containerID == "" {
				containerID = containerIDForPID(procRoot, process.PID, knownContainerIDs)
				hostPID = process.PID
			}
			if hasUptime && hostPID != "" {
				runtimeSeconds, hasRuntime = getProcessRuntimeSeconds(procRoot, hostPID, uptimeSeconds, runtimeCache)
			}

			usedMemory := parseUsedMemoryMiB(process.UsedMemory)

			if containerID == "" {
				slog.Debug("Unable to attribute NVIDIA GPU process to Docker container", "gpu", gpuID, "pid", process.PID, "used_memory", process.UsedMemory)
				consumer := unattributedByGPU[gpuID]
				if consumer == nil {
					consumer = &system.GPUConsumer{
						ID:   "unattributed",
						Name: "Unattributed",
					}
					unattributedByGPU[gpuID] = consumer
				}
				consumer.MemoryUsed = utils.TwoDecimals(consumer.MemoryUsed + usedMemory)
				consumer.ProcessCount++
				if hasRuntime && runtimeSeconds > consumer.RuntimeSeconds {
					consumer.RuntimeSeconds = runtimeSeconds
				}
				continue
			}

			shortID := shortDockerID(containerID)
			name := containerNames[shortID]
			if name == "" {
				name = shortID
			}

			if consumersByGPU[gpuID] == nil {
				consumersByGPU[gpuID] = make(map[string]*system.GPUConsumer)
			}

			consumer, ok := consumersByGPU[gpuID][shortID]
			if !ok {
				consumer = &system.GPUConsumer{
					ID:   shortID,
					Name: name,
				}
				consumersByGPU[gpuID][shortID] = consumer
			}

			consumer.MemoryUsed = utils.TwoDecimals(consumer.MemoryUsed + usedMemory)
			consumer.ProcessCount++
			if hasRuntime && runtimeSeconds > consumer.RuntimeSeconds {
				consumer.RuntimeSeconds = runtimeSeconds
			}
		}
	}

	final := make(map[string][]system.GPUConsumer, len(consumersByGPU))
	for gpuID, consumers := range consumersByGPU {
		list := make([]system.GPUConsumer, 0, len(consumers))
		attributedMemory := 0.0
		for _, consumer := range consumers {
			list = append(list, *consumer)
			attributedMemory += consumer.MemoryUsed
		}
		if consumer := unattributedByGPU[gpuID]; consumer != nil {
			list = append(list, *consumer)
			attributedMemory += consumer.MemoryUsed
			delete(unattributedByGPU, gpuID)
		}
		sort.SliceStable(list, func(i, j int) bool {
			if list[i].MemoryUsed == list[j].MemoryUsed {
				return list[i].Name < list[j].Name
			}
			return list[i].MemoryUsed > list[j].MemoryUsed
		})
		if gpu, ok := gpus[gpuID]; ok && gpu.MemoryUsed > 0 {
			diff := utils.TwoDecimals(gpu.MemoryUsed - attributedMemory)
			if diff > 256 {
				slog.Debug("NVIDIA GPU memory usage exceeds attributed container memory", "gpu", gpuID, "gpu_memory_mb", gpu.MemoryUsed, "attributed_memory_mb", attributedMemory, "difference_mb", diff)
			}
		}
		final[gpuID] = list
	}

	for gpuID, consumer := range unattributedByGPU {
		final[gpuID] = []system.GPUConsumer{*consumer}
		sort.SliceStable(final[gpuID], func(i, j int) bool {
			if final[gpuID][i].MemoryUsed == final[gpuID][j].MemoryUsed {
				return final[gpuID][i].Name < final[gpuID][j].Name
			}
			return final[gpuID][i].MemoryUsed > final[gpuID][j].MemoryUsed
		})
	}

	slog.Debug("NVIDIA GPU consumer attribution result", "gpus_with_consumers", len(final), "total_known_gpus", len(gpus))

	return final
}

func readNvidiaGPUProcesses() (*nvidiaSmiLog, error) {
	ctx, cancel := context.WithTimeout(context.Background(), nvidiaSmiConsumerRead)
	defer cancel()

	if _, err := exec.LookPath(nvidiaSmiCmd); err != nil {
		return nil, err
	}

	cmd := exec.CommandContext(ctx, nvidiaSmiCmd, "-q", "-x")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("nvidia-smi failed: %w: %s", err, strings.TrimSpace(string(output)))
	}

	var data nvidiaSmiLog
	if err := xml.Unmarshal(output, &data); err != nil {
		return nil, err
	}
	return &data, nil
}

func procRootForConsumers() string {
	if utils.FileExists(hostProcRoot) {
		return hostProcRoot
	}
	return "/proc"
}

func resolveNvidiaGPUID(gpu nvidiaSmiGPU, index int, known map[string]system.GPUData) string {
	minor := strings.TrimSpace(gpu.MinorNumber)
	idxStr := strconv.Itoa(index)
	candidates := []string{minor, idxStr}
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		if _, ok := known[candidate]; ok {
			return candidate
		}
	}
	// Log when neither minor_number nor index matches any known GPU.
	knownKeys := make([]string, 0, len(known))
	for k := range known {
		knownKeys = append(knownKeys, k)
	}
	slog.Debug("GPU ID mismatch in NVIDIA process data", "minor_number", minor, "index", idxStr, "known_gpu_ids", knownKeys)
	// Return index as fallback (caller will skip if not in known map).
	return idxStr
}

func containerIDForPID(procRoot, pid string, knownContainerIDs map[string]struct{}) string {
	cgroupPath := filepath.Join(procRoot, pid, "cgroup")
	content, err := utils.ReadStringFileLimited(cgroupPath, 8*1024)
	if err != nil {
		return ""
	}
	return parseDockerIDFromCgroup(content, knownContainerIDs)
}

func parseDockerIDFromCgroup(content string, knownContainerIDs map[string]struct{}) string {
	fallback := ""
	for _, line := range strings.Split(content, "\n") {
		if line == "" {
			continue
		}
		for _, candidate := range cgroupContainerIDCandidates(line) {
			shortID := shortDockerID(candidate)
			if len(knownContainerIDs) > 0 {
				if _, ok := knownContainerIDs[candidate]; ok {
					return candidate
				}
				if _, ok := knownContainerIDs[shortID]; ok {
					return candidate
				}
			}
			if fallback == "" {
				fallback = candidate
			}
		}
	}
	return fallback
}

func cgroupContainerIDCandidates(line string) []string {
	candidates := make([]string, 0, 4)
	seen := make(map[string]struct{}, 4)
	addCandidate := func(candidate string) {
		candidate = strings.TrimSpace(strings.TrimSuffix(candidate, ".scope"))
		if !dockerContainerIDPattern.MatchString(candidate) {
			return
		}
		if _, ok := seen[candidate]; ok {
			return
		}
		seen[candidate] = struct{}{}
		candidates = append(candidates, candidate)
	}

	if match := dockerPathPattern.FindStringSubmatch(line); len(match) == 2 {
		addCandidate(match[1])
	}
	if match := dockerScopePattern.FindStringSubmatch(line); len(match) == 2 {
		addCandidate(match[1])
	}

	path := line
	if idx := strings.LastIndex(line, ":"); idx >= 0 {
		path = line[idx+1:]
	}
	for _, segment := range strings.Split(path, "/") {
		segment = strings.TrimSpace(segment)
		if segment == "" {
			continue
		}

		addCandidate(segment)

		scopeSegment := strings.TrimSuffix(segment, ".scope")
		for _, prefix := range containerCgroupPrefixes {
			if strings.HasPrefix(scopeSegment, prefix) {
				addCandidate(strings.TrimPrefix(scopeSegment, prefix))
			}
		}
	}

	return candidates
}

func shortDockerID(containerID string) string {
	if len(containerID) > 12 {
		return containerID[:12]
	}
	return containerID
}

func parseUsedMemoryMiB(raw string) float64 {
	fields := strings.Fields(raw)
	if len(fields) == 0 {
		return 0
	}
	value, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0
	}
	return utils.TwoDecimals(value / mebibytesInAMegabyte)
}


func readProcUptimeSeconds(procRoot string) (float64, bool) {
	uptimePath := filepath.Join(procRoot, "uptime")
	content, err := utils.ReadStringFileLimited(uptimePath, 256)
	if err != nil {
		return 0, false
	}
	fields := strings.Fields(content)
	if len(fields) == 0 {
		return 0, false
	}
	uptimeSeconds, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0, false
	}
	return uptimeSeconds, true
}

func getProcessRuntimeSeconds(procRoot, pid string, uptimeSeconds float64, runtimeCache map[string]uint64) (uint64, bool) {
	pid = strings.TrimSpace(pid)
	if pid == "" {
		return 0, false
	}
	if runtime, ok := runtimeCache[pid]; ok {
		return runtime, true
	}
	startTimeSeconds, ok := readProcessStartTimeSeconds(procRoot, pid)
	if !ok || startTimeSeconds > uptimeSeconds {
		return 0, false
	}
	runtimeSeconds := uint64(math.Max(0, math.Floor(uptimeSeconds-startTimeSeconds)))
	runtimeCache[pid] = runtimeSeconds
	return runtimeSeconds, true
}

func readProcessStartTimeSeconds(procRoot, pid string) (float64, bool) {
	statPath := filepath.Join(procRoot, pid, "stat")
	content, err := utils.ReadStringFileLimited(statPath, 4096)
	if err != nil {
		return 0, false
	}
	endIdx := strings.LastIndex(content, ") ")
	if endIdx == -1 {
		return 0, false
	}
	fields := strings.Fields(content[endIdx+2:])
	if len(fields) <= 19 {
		return 0, false
	}
	startTicks, err := strconv.ParseFloat(fields[19], 64)
	if err != nil {
		return 0, false
	}
	clockTicks := getClockTicksPerSecond()
	if clockTicks <= 0 {
		return 0, false
	}
	return startTicks / clockTicks, true
}

func getClockTicksPerSecond() float64 {
	clockTicksOnce.Do(func() {
		output, err := exec.Command("getconf", "CLK_TCK").Output()
		if err != nil {
			return
		}
		value, err := strconv.ParseFloat(strings.TrimSpace(string(output)), 64)
		if err == nil && value > 0 {
			clockTicksPerSec = value
		}
	})
	return clockTicksPerSec
}
