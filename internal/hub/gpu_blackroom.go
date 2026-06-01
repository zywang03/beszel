package hub

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/henrygd/beszel/internal/entities/system"
	"github.com/henrygd/beszel/internal/hub/systems"
	"gopkg.in/yaml.v3"
)

const (
	gpuBlackroomActionStopThenStart = "stop_then_start"
	gpuBlackroomStateStopping       = "stopping"
	gpuBlackroomStateCoolingDown    = "cooling_down"
	gpuBlackroomStateRestarting     = "restarting"
	gpuBlackroomStateCompleted      = "completed"
	gpuBlackroomStateFailed         = "failed"
)

type gpuBlackroomConfig struct {
	Enabled         bool                        `json:"enabled"`
	ConfigPath      string                      `json:"configPath,omitempty"`
	DefaultCooldown time.Duration               `json:"-"`
	Rules           map[string]gpuBlackroomRule `json:"rules"`
	LoadedAt        time.Time                   `json:"loadedAt"`
	Errors          []string                    `json:"errors,omitempty"`
}

type gpuBlackroomRule struct {
	ContainerName string        `json:"containerName" yaml:"-"`
	MaxGPU        int           `json:"maxGpu" yaml:"max_gpu"`
	Action        string        `json:"action" yaml:"action"`
	Cooldown      time.Duration `json:"-" yaml:"-"`
	CooldownText  string        `json:"cooldown" yaml:"cooldown"`
	Enabled       bool          `json:"enabled" yaml:"enabled"`
}

type gpuBlackroomConfigFile struct {
	Containers map[string]gpuBlackroomRule `yaml:"containers"`
}

type gpuBlackroomSystemData = systems.GPUBlackroomSystemData

type gpuBlackroomSnapshot struct {
	Containers map[string]gpuBlackroomUsage `json:"containers"`
}

type gpuBlackroomUsage struct {
	ContainerName string                            `json:"containerName"`
	GPUCount      int                               `json:"gpuCount"`
	Candidates    map[string]*gpuBlackroomCandidate `json:"-"`
}

type gpuBlackroomCandidate struct {
	SystemID       string `json:"systemId"`
	SystemName     string `json:"systemName"`
	ContainerName  string `json:"containerName"`
	ContainerID    string `json:"containerId"`
	GPUCount       int    `json:"gpuCount"`
	RuntimeSeconds uint64 `json:"runtimeSeconds,omitempty"`
	HasRuntime     bool   `json:"hasRuntime"`
	Sequence       int    `json:"-"`
}

type gpuBlackroomDecision struct {
	ShouldEnforce bool                  `json:"shouldEnforce"`
	Rule          gpuBlackroomRule      `json:"rule"`
	Usage         gpuBlackroomUsage     `json:"usage"`
	Candidate     gpuBlackroomCandidate `json:"candidate"`
	Reason        string                `json:"reason,omitempty"`
}

type gpuBlackroomEnforcement struct {
	ContainerName  string    `json:"containerName"`
	SystemID       string    `json:"systemId"`
	SystemName     string    `json:"systemName"`
	ContainerID    string    `json:"containerId"`
	ObservedGPU    int       `json:"observedGpu"`
	LimitGPU       int       `json:"limitGpu"`
	State          string    `json:"state"`
	StoppedAt      time.Time `json:"stoppedAt,omitempty"`
	RestartAt      time.Time `json:"restartAt,omitempty"`
	CompletedAt    time.Time `json:"completedAt,omitempty"`
	LastError      string    `json:"lastError,omitempty"`
	RuntimeSeconds uint64    `json:"runtimeSeconds,omitempty"`
}

func (e gpuBlackroomEnforcement) key() string {
	return strings.Join([]string{e.ContainerName, e.SystemID, e.ContainerID}, "\x00")
}

type gpuBlackroomManager struct {
	hub       *Hub
	config    gpuBlackroomConfig
	statePath string

	mu     sync.Mutex
	active map[string]gpuBlackroomEnforcement
	recent []gpuBlackroomEnforcement
	timers map[string]*time.Timer

	now       func() time.Time
	afterFunc func(time.Duration, func()) *time.Timer
}

type gpuBlackroomPersistedState struct {
	Active []gpuBlackroomEnforcement `json:"active"`
	Recent []gpuBlackroomEnforcement `json:"recent"`
}

func newGPUBlackroomManager(h *Hub, cfg gpuBlackroomConfig) *gpuBlackroomManager {
	return &gpuBlackroomManager{
		hub:       h,
		config:    cfg,
		active:    make(map[string]gpuBlackroomEnforcement),
		timers:    make(map[string]*time.Timer),
		now:       time.Now,
		afterFunc: time.AfterFunc,
	}
}

func loadGPUBlackroomConfig(path string, globalEnabled bool, defaultCooldown time.Duration) (gpuBlackroomConfig, error) {
	cfg := gpuBlackroomConfig{
		Enabled:         globalEnabled,
		ConfigPath:      path,
		DefaultCooldown: defaultCooldown,
		Rules:           make(map[string]gpuBlackroomRule),
		LoadedAt:        time.Now(),
	}
	if defaultCooldown == 0 {
		cfg.DefaultCooldown = 10 * time.Minute
	}
	if strings.TrimSpace(path) == "" {
		return cfg, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, err
	}

	var file gpuBlackroomConfigFile
	if err := yaml.Unmarshal(data, &file); err != nil {
		return cfg, err
	}

	for name, rule := range file.Containers {
		rule.ContainerName = strings.TrimSpace(name)
		if rule.ContainerName == "" {
			return cfg, errors.New("gpu blackroom rule has empty container name")
		}
		if rule.Action == "" {
			rule.Action = gpuBlackroomActionStopThenStart
		}
		if rule.Action != gpuBlackroomActionStopThenStart {
			return cfg, fmt.Errorf("unsupported gpu blackroom action for %s: %s", name, rule.Action)
		}
		if rule.CooldownText == "" {
			rule.Cooldown = cfg.DefaultCooldown
			rule.CooldownText = rule.Cooldown.String()
		} else {
			cooldown, err := time.ParseDuration(rule.CooldownText)
			if err != nil {
				return cfg, fmt.Errorf("invalid gpu blackroom cooldown for %s: %w", name, err)
			}
			rule.Cooldown = cooldown
		}
		if rule.Enabled {
			if rule.MaxGPU <= 0 {
				return cfg, fmt.Errorf("gpu blackroom max_gpu for %s must be positive", name)
			}
			if rule.Cooldown < time.Minute {
				return cfg, fmt.Errorf("gpu blackroom cooldown for %s must be at least 1m", name)
			}
		}
		cfg.Rules[rule.ContainerName] = rule
	}

	return cfg, nil
}

func loadGPUBlackroomConfigFromEnv() gpuBlackroomConfig {
	enabled := parseBoolEnv("GPU_BLACKROOM_ENABLED", false)
	defaultCooldown := parseDurationEnv("GPU_BLACKROOM_DEFAULT_COOLDOWN", 10*time.Minute)
	path, _ := GetEnv("GPU_BLACKROOM_CONFIG")
	cfg, err := loadGPUBlackroomConfig(path, enabled, defaultCooldown)
	if err != nil {
		cfg.Enabled = false
		cfg.ConfigPath = path
		cfg.DefaultCooldown = defaultCooldown
		cfg.Rules = make(map[string]gpuBlackroomRule)
		cfg.Errors = append(cfg.Errors, err.Error())
		slog.Error("Failed to load GPU blackroom config", "err", err)
	}
	return cfg
}

func parseBoolEnv(key string, fallback bool) bool {
	value, ok := GetEnv(key)
	if !ok {
		return fallback
	}
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
}

func parseDurationEnv(key string, fallback time.Duration) time.Duration {
	value, ok := GetEnv(key)
	if !ok || strings.TrimSpace(value) == "" {
		return fallback
	}
	duration, err := time.ParseDuration(value)
	if err != nil {
		return fallback
	}
	return duration
}

func collectGPUBlackroomSnapshot(rules map[string]gpuBlackroomRule, data map[string]*gpuBlackroomSystemData) gpuBlackroomSnapshot {
	snapshot := gpuBlackroomSnapshot{Containers: make(map[string]gpuBlackroomUsage)}
	if len(rules) == 0 || len(data) == 0 {
		return snapshot
	}

	sequence := 0
	systemIDs := make([]string, 0, len(data))
	for systemID := range data {
		systemIDs = append(systemIDs, systemID)
	}
	sort.Strings(systemIDs)

	for _, systemID := range systemIDs {
		systemData := data[systemID]
		if systemData == nil || systemData.Data == nil || len(systemData.Data.Info.GPUSummaries) == 0 {
			continue
		}
		gpuIDs := make([]string, 0, len(systemData.Data.Info.GPUSummaries))
		for gpuID := range systemData.Data.Info.GPUSummaries {
			gpuIDs = append(gpuIDs, gpuID)
		}
		sort.Strings(gpuIDs)
		for _, gpuID := range gpuIDs {
			gpu := systemData.Data.Info.GPUSummaries[gpuID]
			seenOnGPU := make(map[string]struct{})
			for _, consumer := range gpu.Consumers {
				rule, ok := rules[consumer.Name]
				if !ok || !rule.Enabled {
					continue
				}
				if _, seen := seenOnGPU[consumer.Name]; seen {
					updateGPUBlackroomCandidateRuntime(snapshot, consumer.Name, systemID, consumer)
					continue
				}
				seenOnGPU[consumer.Name] = struct{}{}

				usage := snapshot.Containers[consumer.Name]
				if usage.ContainerName == "" {
					usage.ContainerName = consumer.Name
				}
				if usage.Candidates == nil {
					usage.Candidates = make(map[string]*gpuBlackroomCandidate)
				}
				usage.GPUCount++

				containerID := strings.TrimSpace(consumer.ID)
				if containerID == "" {
					containerID = consumer.Name
				}
				candidateKey := systemID + "/" + containerID
				candidate := usage.Candidates[candidateKey]
				if candidate == nil {
					sequence++
					candidate = &gpuBlackroomCandidate{
						SystemID:      systemID,
						SystemName:    systemData.SystemName,
						ContainerName: consumer.Name,
						ContainerID:   containerID,
						Sequence:      sequence,
					}
					usage.Candidates[candidateKey] = candidate
				}
				candidate.GPUCount++
				if consumer.RuntimeSeconds > 0 && (!candidate.HasRuntime || consumer.RuntimeSeconds < candidate.RuntimeSeconds) {
					candidate.RuntimeSeconds = consumer.RuntimeSeconds
					candidate.HasRuntime = true
				}
				snapshot.Containers[consumer.Name] = usage
			}
		}
	}

	return snapshot
}

func updateGPUBlackroomCandidateRuntime(snapshot gpuBlackroomSnapshot, containerName string, systemID string, consumer system.GPUConsumer) {
	usage := snapshot.Containers[containerName]
	if usage.Candidates == nil {
		return
	}
	containerID := strings.TrimSpace(consumer.ID)
	if containerID == "" {
		containerID = consumer.Name
	}
	candidate := usage.Candidates[systemID+"/"+containerID]
	if candidate == nil || consumer.RuntimeSeconds == 0 {
		return
	}
	if !candidate.HasRuntime || consumer.RuntimeSeconds < candidate.RuntimeSeconds {
		candidate.RuntimeSeconds = consumer.RuntimeSeconds
		candidate.HasRuntime = true
	}
}

func selectGPUBlackroomCandidate(usage gpuBlackroomUsage) (gpuBlackroomCandidate, bool) {
	if len(usage.Candidates) == 0 {
		return gpuBlackroomCandidate{}, false
	}
	candidates := make([]*gpuBlackroomCandidate, 0, len(usage.Candidates))
	for _, candidate := range usage.Candidates {
		if candidate != nil {
			candidates = append(candidates, candidate)
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		left := candidates[i]
		right := candidates[j]
		if left.HasRuntime != right.HasRuntime {
			return left.HasRuntime
		}
		if left.HasRuntime && right.HasRuntime && left.RuntimeSeconds != right.RuntimeSeconds {
			return left.RuntimeSeconds < right.RuntimeSeconds
		}
		return left.Sequence > right.Sequence
	})
	return *candidates[0], true
}

func (m *gpuBlackroomManager) evaluateSnapshot(snapshot gpuBlackroomSnapshot) gpuBlackroomDecision {
	if m == nil || !m.config.Enabled {
		return gpuBlackroomDecision{Reason: "gpu blackroom disabled"}
	}
	ruleNames := make([]string, 0, len(m.config.Rules))
	for name := range m.config.Rules {
		ruleNames = append(ruleNames, name)
	}
	sort.Strings(ruleNames)
	for _, name := range ruleNames {
		rule := m.config.Rules[name]
		if !rule.Enabled {
			continue
		}
		usage := snapshot.Containers[name]
		if usage.GPUCount <= rule.MaxGPU {
			continue
		}
		candidate, ok := selectGPUBlackroomCandidate(usage)
		if !ok {
			continue
		}
		if m.hasActiveEnforcement(candidate) {
			continue
		}
		return gpuBlackroomDecision{
			ShouldEnforce: true,
			Rule:          rule,
			Usage:         usage,
			Candidate:     candidate,
			Reason:        "gpu quota exceeded",
		}
	}
	return gpuBlackroomDecision{Reason: "no gpu quota exceeded"}
}

func (m *gpuBlackroomManager) hasActiveEnforcement(candidate gpuBlackroomCandidate) bool {
	if m == nil {
		return false
	}
	key := gpuBlackroomEnforcement{
		ContainerName: candidate.ContainerName,
		SystemID:      candidate.SystemID,
		ContainerID:   candidate.ContainerID,
	}.key()
	m.mu.Lock()
	defer m.mu.Unlock()
	enforcement, ok := m.active[key]
	if !ok {
		return false
	}
	return enforcement.State == gpuBlackroomStateStopping ||
		enforcement.State == gpuBlackroomStateCoolingDown ||
		enforcement.State == gpuBlackroomStateRestarting
}

func (m *gpuBlackroomManager) stateFilePath() string {
	if m == nil {
		return ""
	}
	if m.statePath != "" {
		return m.statePath
	}
	if m.hub == nil {
		return ""
	}
	return filepath.Join(m.hub.DataDir(), "gpu_blackroom_state.json")
}

func (m *gpuBlackroomManager) marshalStateLocked() ([]byte, error) {
	state := gpuBlackroomPersistedState{
		Active: make([]gpuBlackroomEnforcement, 0, len(m.active)),
		Recent: append([]gpuBlackroomEnforcement(nil), m.recent...),
	}
	for _, enforcement := range m.active {
		state.Active = append(state.Active, enforcement)
	}
	sort.Slice(state.Active, func(i, j int) bool {
		return state.Active[i].key() < state.Active[j].key()
	})
	return json.MarshalIndent(state, "", "  ")
}

func (m *gpuBlackroomManager) persistState() error {
	if m == nil {
		return nil
	}
	path := m.stateFilePath()
	if path == "" {
		return nil
	}
	m.mu.Lock()
	data, err := m.marshalStateLocked()
	m.mu.Unlock()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func (m *gpuBlackroomManager) loadState() error {
	if m == nil {
		return nil
	}
	path := m.stateFilePath()
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var state gpuBlackroomPersistedState
	if err := json.Unmarshal(data, &state); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.active = make(map[string]gpuBlackroomEnforcement, len(state.Active))
	for _, enforcement := range state.Active {
		if enforcement.ContainerName == "" || enforcement.SystemID == "" || enforcement.ContainerID == "" {
			continue
		}
		m.active[enforcement.key()] = enforcement
	}
	m.recent = append([]gpuBlackroomEnforcement(nil), state.Recent...)
	return nil
}

func (m *gpuBlackroomManager) enforceDecision(decision gpuBlackroomDecision, control func(containerID string, operation string) error) error {
	if m == nil || !decision.ShouldEnforce {
		return nil
	}
	if control == nil {
		return errors.New("gpu blackroom control function is nil")
	}
	candidate := decision.Candidate
	if candidate.ContainerID == "" {
		return errors.New("gpu blackroom candidate missing container id")
	}
	enforcement := m.enforcementFromDecision(decision, gpuBlackroomStateStopping)
	m.mu.Lock()
	if current, exists := m.active[enforcement.key()]; exists && (current.State == gpuBlackroomStateStopping || current.State == gpuBlackroomStateCoolingDown || current.State == gpuBlackroomStateRestarting) {
		m.mu.Unlock()
		return nil
	}
	m.active[enforcement.key()] = enforcement
	m.mu.Unlock()
	_ = m.persistState()

	if err := control(candidate.ContainerID, "stop"); err != nil {
		enforcement.LastError = err.Error()
		enforcement.CompletedAt = m.now()
		enforcement.State = gpuBlackroomStateFailed
		m.mu.Lock()
		delete(m.active, enforcement.key())
		m.mu.Unlock()
		m.recordRecent(enforcement)
		_ = m.persistState()
		return err
	}

	enforcement.State = gpuBlackroomStateCoolingDown
	enforcement.StoppedAt = m.now()
	enforcement.RestartAt = enforcement.StoppedAt.Add(decision.Rule.Cooldown)
	m.mu.Lock()
	m.active[enforcement.key()] = enforcement
	m.mu.Unlock()
	if err := m.persistState(); err != nil {
		return err
	}
	m.scheduleRestart(enforcement, control)
	return nil
}

func (m *gpuBlackroomManager) enforcementFromDecision(decision gpuBlackroomDecision, state string) gpuBlackroomEnforcement {
	return gpuBlackroomEnforcement{
		ContainerName:  decision.Candidate.ContainerName,
		SystemID:       decision.Candidate.SystemID,
		SystemName:     decision.Candidate.SystemName,
		ContainerID:    decision.Candidate.ContainerID,
		ObservedGPU:    decision.Usage.GPUCount,
		LimitGPU:       decision.Rule.MaxGPU,
		State:          state,
		RuntimeSeconds: decision.Candidate.RuntimeSeconds,
	}
}

func (m *gpuBlackroomManager) scheduleRestart(enforcement gpuBlackroomEnforcement, control func(containerID string, operation string) error) {
	key := enforcement.key()
	m.mu.Lock()
	if _, exists := m.timers[key]; exists {
		m.mu.Unlock()
		return
	}
	m.mu.Unlock()

	delay := time.Until(enforcement.RestartAt)
	if m.now != nil {
		delay = enforcement.RestartAt.Sub(m.now())
	}
	if delay < 0 {
		delay = 0
	}
	if m.afterFunc == nil {
		m.afterFunc = time.AfterFunc
	}
	timer := m.afterFunc(delay, func() {
		m.restartEnforcement(enforcement, control)
	})
	if timer != nil {
		m.mu.Lock()
		m.timers[key] = timer
		m.mu.Unlock()
	}
}

func (m *gpuBlackroomManager) restartEnforcement(enforcement gpuBlackroomEnforcement, control func(containerID string, operation string) error) {
	key := enforcement.key()
	m.mu.Lock()
	delete(m.timers, key)
	current, ok := m.active[key]
	if !ok {
		m.mu.Unlock()
		return
	}
	current.State = gpuBlackroomStateRestarting
	m.active[key] = current
	m.mu.Unlock()
	_ = m.persistState()

	if err := control(current.ContainerID, "start"); err != nil {
		current.State = gpuBlackroomStateFailed
		current.LastError = err.Error()
		current.CompletedAt = m.now()
		m.mu.Lock()
		m.active[key] = current
		m.mu.Unlock()
		_ = m.persistState()
		return
	}

	current.State = gpuBlackroomStateCompleted
	current.CompletedAt = m.now()
	m.mu.Lock()
	delete(m.active, key)
	m.appendRecentLocked(current)
	m.mu.Unlock()
	_ = m.persistState()
}

func (m *gpuBlackroomManager) recordRecent(enforcement gpuBlackroomEnforcement) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.appendRecentLocked(enforcement)
}

func (m *gpuBlackroomManager) appendRecentLocked(enforcement gpuBlackroomEnforcement) {
	m.recent = append([]gpuBlackroomEnforcement{enforcement}, m.recent...)
	if len(m.recent) > 50 {
		m.recent = m.recent[:50]
	}
}

func (m *gpuBlackroomManager) collectSystemData() map[string]*gpuBlackroomSystemData {
	if m == nil || m.hub == nil || m.hub.sm == nil {
		return nil
	}
	return m.hub.sm.GPUBlackroomSystemData()
}

func (m *gpuBlackroomManager) Evaluate() gpuBlackroomDecision {
	data := m.collectSystemData()
	m.recoverActiveRestarts(data)
	snapshot := collectGPUBlackroomSnapshot(m.config.Rules, data)
	decision := m.evaluateSnapshot(snapshot)
	if !decision.ShouldEnforce {
		return decision
	}
	systemData := data[decision.Candidate.SystemID]
	if systemData == nil || systemData.Control == nil {
		decision.ShouldEnforce = false
		decision.Reason = "selected system cannot control containers"
		return decision
	}
	if err := m.enforceDecision(decision, systemData.Control); err != nil {
		slog.Error("GPU blackroom enforcement failed", "container", decision.Candidate.ContainerName, "system", decision.Candidate.SystemName, "err", err)
		decision.Reason = err.Error()
		return decision
	}
	slog.Warn("GPU blackroom enforced quota", "container", decision.Candidate.ContainerName, "system", decision.Candidate.SystemName, "container_id", decision.Candidate.ContainerID, "gpu_count", decision.Usage.GPUCount, "max_gpu", decision.Rule.MaxGPU)
	return decision
}

func (m *gpuBlackroomManager) Start() {
	if m == nil {
		return
	}
	if err := m.loadState(); err != nil {
		slog.Error("Failed to load GPU blackroom state", "err", err)
	}
	if !m.config.Enabled {
		return
	}
	m.recoverActiveRestarts(m.collectSystemData())
}

func (m *gpuBlackroomManager) recoverActiveRestarts(data map[string]*gpuBlackroomSystemData) {
	if m == nil || len(data) == 0 {
		return
	}
	m.mu.Lock()
	active := make([]gpuBlackroomEnforcement, 0, len(m.active))
	for _, enforcement := range m.active {
		if enforcement.State != gpuBlackroomStateCoolingDown && enforcement.State != gpuBlackroomStateRestarting {
			continue
		}
		active = append(active, enforcement)
	}
	m.mu.Unlock()
	for _, enforcement := range active {
		systemData := data[enforcement.SystemID]
		if systemData == nil || systemData.Control == nil {
			continue
		}
		m.scheduleRestart(enforcement, systemData.Control)
	}
}

func (m *gpuBlackroomManager) Status() map[string]any {
	if m == nil {
		return map[string]any{"enabled": false}
	}
	m.mu.Lock()
	active := make([]gpuBlackroomEnforcement, 0, len(m.active))
	for _, enforcement := range m.active {
		active = append(active, enforcement)
	}
	recent := append([]gpuBlackroomEnforcement(nil), m.recent...)
	m.mu.Unlock()
	sort.Slice(active, func(i, j int) bool {
		return active[i].RestartAt.Before(active[j].RestartAt)
	})
	return map[string]any{
		"enabled":    m.config.Enabled,
		"configPath": m.config.ConfigPath,
		"rules":      m.config.Rules,
		"active":     active,
		"recent":     recent,
		"errors":     m.config.Errors,
	}
}
