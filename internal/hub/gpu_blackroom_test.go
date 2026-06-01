//go:build testing

package hub

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/henrygd/beszel/internal/entities/system"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadGPUBlackroomConfig(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "blackroom.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte(`
containers:
  22228-liuyk:
    max_gpu: 8
    action: stop_then_start
    cooldown: 10m
    enabled: true
  disabled-job:
    max_gpu: 1
    enabled: false
`), 0600))

	cfg, err := loadGPUBlackroomConfig(configPath, true, 15*time.Minute)

	require.NoError(t, err)
	assert.True(t, cfg.Enabled)
	require.Contains(t, cfg.Rules, "22228-liuyk")
	rule := cfg.Rules["22228-liuyk"]
	assert.Equal(t, 8, rule.MaxGPU)
	assert.Equal(t, gpuBlackroomActionStopThenStart, rule.Action)
	assert.Equal(t, 10*time.Minute, rule.Cooldown)
	assert.True(t, rule.Enabled)
	assert.False(t, cfg.Rules["disabled-job"].Enabled)
}

func TestGPUBlackroomCountsDistinctGPUsAcrossSystems(t *testing.T) {
	rules := map[string]gpuBlackroomRule{
		"22228-liuyk": {
			ContainerName: "22228-liuyk",
			MaxGPU:        2,
			Enabled:       true,
			Action:        gpuBlackroomActionStopThenStart,
			Cooldown:      10 * time.Minute,
		},
	}
	data := map[string]*gpuBlackroomSystemData{
		"sys-a": {
			SystemID:   "sys-a",
			SystemName: "server-a",
			Data: gpuBlackroomCombinedData(map[string]system.GPUData{
				"0": gpuBlackroomGPU("22228-liuyk", "aaaa11112222", 500),
				"1": gpuBlackroomGPU("22228-liuyk", "aaaa11112222", 400),
			}),
		},
		"sys-b": {
			SystemID:   "sys-b",
			SystemName: "server-b",
			Data: gpuBlackroomCombinedData(map[string]system.GPUData{
				"0": gpuBlackroomGPU("22228-liuyk", "bbbb11112222", 10),
			}),
		},
	}

	snapshot := collectGPUBlackroomSnapshot(rules, data)

	require.Contains(t, snapshot.Containers, "22228-liuyk")
	usage := snapshot.Containers["22228-liuyk"]
	assert.Equal(t, 3, usage.GPUCount)
	assert.Len(t, usage.Candidates, 2)
}

func TestGPUBlackroomSelectsShortestRuntimeCandidate(t *testing.T) {
	usage := gpuBlackroomUsage{
		ContainerName: "22228-liuyk",
		GPUCount:      3,
		Candidates: map[string]*gpuBlackroomCandidate{
			"sys-a/aaaa11112222": {
				SystemID:       "sys-a",
				SystemName:     "server-a",
				ContainerName:  "22228-liuyk",
				ContainerID:    "aaaa11112222",
				GPUCount:       2,
				RuntimeSeconds: 500,
				HasRuntime:     true,
			},
			"sys-b/bbbb11112222": {
				SystemID:       "sys-b",
				SystemName:     "server-b",
				ContainerName:  "22228-liuyk",
				ContainerID:    "bbbb11112222",
				GPUCount:       1,
				RuntimeSeconds: 10,
				HasRuntime:     true,
			},
		},
	}

	candidate, ok := selectGPUBlackroomCandidate(usage)

	require.True(t, ok)
	assert.Equal(t, "sys-b", candidate.SystemID)
	assert.Equal(t, "bbbb11112222", candidate.ContainerID)
}

func TestGPUBlackroomDoesNotTriggerWhenAtLimit(t *testing.T) {
	manager := newGPUBlackroomManager(nil, gpuBlackroomConfig{
		Enabled: true,
		Rules: map[string]gpuBlackroomRule{
			"22228-liuyk": {
				ContainerName: "22228-liuyk",
				MaxGPU:        2,
				Enabled:       true,
				Action:        gpuBlackroomActionStopThenStart,
				Cooldown:      10 * time.Minute,
			},
		},
	})
	data := map[string]*gpuBlackroomSystemData{
		"sys-a": {
			SystemID:   "sys-a",
			SystemName: "server-a",
			Data: gpuBlackroomCombinedData(map[string]system.GPUData{
				"0": gpuBlackroomGPU("22228-liuyk", "aaaa11112222", 500),
				"1": gpuBlackroomGPU("22228-liuyk", "aaaa11112222", 400),
			}),
		},
	}

	decision := manager.evaluateSnapshot(collectGPUBlackroomSnapshot(manager.config.Rules, data))

	assert.False(t, decision.ShouldEnforce)
}

func TestGPUBlackroomSuppressesDuplicateActiveEnforcement(t *testing.T) {
	manager := newGPUBlackroomManager(nil, gpuBlackroomConfig{Enabled: true})
	enforcement := gpuBlackroomEnforcement{
		ContainerName: "22228-liuyk",
		SystemID:      "sys-a",
		ContainerID:   "aaaa11112222",
		State:         gpuBlackroomStateCoolingDown,
	}
	manager.active[enforcement.key()] = enforcement

	duplicate := gpuBlackroomCandidate{
		SystemID:      "sys-a",
		ContainerName: "22228-liuyk",
		ContainerID:   "aaaa11112222",
	}

	assert.True(t, manager.hasActiveEnforcement(duplicate))
}

func TestGPUBlackroomPersistsAndLoadsActiveEnforcement(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	manager := newGPUBlackroomManager(nil, gpuBlackroomConfig{Enabled: true})
	manager.statePath = statePath
	enforcement := gpuBlackroomEnforcement{
		ContainerName: "22228-liuyk",
		SystemID:      "sys-a",
		SystemName:    "server-a",
		ContainerID:   "aaaa11112222",
		ObservedGPU:   9,
		LimitGPU:      8,
		State:         gpuBlackroomStateCoolingDown,
		StoppedAt:     time.Date(2026, 6, 2, 10, 0, 0, 0, time.UTC),
		RestartAt:     time.Date(2026, 6, 2, 10, 10, 0, 0, time.UTC),
	}
	manager.active[enforcement.key()] = enforcement

	require.NoError(t, manager.persistState())

	reloaded := newGPUBlackroomManager(nil, gpuBlackroomConfig{Enabled: true})
	reloaded.statePath = statePath
	require.NoError(t, reloaded.loadState())
	require.Contains(t, reloaded.active, enforcement.key())
	assert.Equal(t, gpuBlackroomStateCoolingDown, reloaded.active[enforcement.key()].State)
	assert.Equal(t, 9, reloaded.active[enforcement.key()].ObservedGPU)
}

func TestGPUBlackroomSchedulesRestartAfterStop(t *testing.T) {
	now := time.Date(2026, 6, 2, 10, 0, 0, 0, time.UTC)
	manager := newGPUBlackroomManager(nil, gpuBlackroomConfig{Enabled: true})
	manager.now = func() time.Time { return now }
	manager.statePath = filepath.Join(t.TempDir(), "state.json")

	var scheduled time.Duration
	var scheduledFn func()
	manager.afterFunc = func(d time.Duration, fn func()) *time.Timer {
		scheduled = d
		scheduledFn = fn
		return nil
	}

	var calls []string
	control := func(containerID string, operation string) error {
		calls = append(calls, operation+":"+containerID)
		return nil
	}
	decision := gpuBlackroomDecision{
		ShouldEnforce: true,
		Rule: gpuBlackroomRule{
			ContainerName: "22228-liuyk",
			MaxGPU:        8,
			Action:        gpuBlackroomActionStopThenStart,
			Cooldown:      10 * time.Minute,
			Enabled:       true,
		},
		Usage: gpuBlackroomUsage{GPUCount: 9},
		Candidate: gpuBlackroomCandidate{
			SystemID:      "sys-a",
			SystemName:    "server-a",
			ContainerName: "22228-liuyk",
			ContainerID:   "aaaa11112222",
		},
	}

	require.NoError(t, manager.enforceDecision(decision, control))

	assert.Equal(t, []string{"stop:aaaa11112222"}, calls)
	assert.Equal(t, 10*time.Minute, scheduled)
	require.NotNil(t, scheduledFn)
	assert.Len(t, manager.active, 1)

	scheduledFn()

	assert.Equal(t, []string{"stop:aaaa11112222", "start:aaaa11112222"}, calls)
	assert.Empty(t, manager.active)
}

func TestGPUBlackroomStatusIncludesConfigAndState(t *testing.T) {
	manager := newGPUBlackroomManager(nil, gpuBlackroomConfig{
		Enabled:    true,
		ConfigPath: "/tmp/blackroom.yaml",
		Rules: map[string]gpuBlackroomRule{
			"22228-liuyk": {
				ContainerName: "22228-liuyk",
				MaxGPU:        8,
				Action:        gpuBlackroomActionStopThenStart,
				Cooldown:      10 * time.Minute,
				CooldownText:  "10m",
				Enabled:       true,
			},
		},
	})
	enforcement := gpuBlackroomEnforcement{
		ContainerName: "22228-liuyk",
		SystemID:      "sys-a",
		ContainerID:   "aaaa11112222",
		State:         gpuBlackroomStateCoolingDown,
	}
	manager.active[enforcement.key()] = enforcement

	status := manager.Status()

	assert.Equal(t, true, status["enabled"])
	assert.Equal(t, "/tmp/blackroom.yaml", status["configPath"])
	assert.NotEmpty(t, status["rules"])
	assert.NotEmpty(t, status["active"])
}

func gpuBlackroomCombinedData(gpus map[string]system.GPUData) *system.CombinedData {
	return &system.CombinedData{
		Info: system.Info{
			GPUSummaries: gpus,
		},
	}
}

func gpuBlackroomGPU(name string, id string, runtime uint64) system.GPUData {
	return system.GPUData{
		Consumers: []system.GPUConsumer{
			{
				ID:             id,
				Name:           name,
				RuntimeSeconds: runtime,
				ProcessCount:   1,
			},
			{
				ID:             id,
				Name:           name,
				RuntimeSeconds: runtime + 1,
				ProcessCount:   1,
			},
		},
	}
}
