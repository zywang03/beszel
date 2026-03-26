//go:build testing

package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseDockerIDFromCgroup(t *testing.T) {
	t.Run("matches docker cgroup v1 path", func(t *testing.T) {
		content := "11:memory:/docker/0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
		got := parseDockerIDFromCgroup(content, nil)
		assert.Equal(t, "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", got)
	})

	t.Run("matches systemd docker scope", func(t *testing.T) {
		content := "0::/system.slice/docker-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.scope"
		got := parseDockerIDFromCgroup(content, nil)
		assert.Equal(t, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", got)
	})

	t.Run("matches containerd scope when known container id is provided", func(t *testing.T) {
		content := "0::/kubepods.slice/kubepods-burstable.slice/cri-containerd-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb.scope"
		got := parseDockerIDFromCgroup(content, map[string]struct{}{
			"bbbbbbbbbbbb": {},
		})
		assert.Equal(t, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", got)
	})

	t.Run("prefers known docker container id when multiple ids exist in cgroup path", func(t *testing.T) {
		content := "0::/kubepods.slice/podaaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee/cri-containerd-cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc.scope/docker/dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
		got := parseDockerIDFromCgroup(content, map[string]struct{}{
			"dddddddddddd": {},
		})
		assert.Equal(t, "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd", got)
	})

	t.Run("falls back to first detected container id when known ids are unavailable", func(t *testing.T) {
		content := "0::/machine.slice/libpod-eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee.scope"
		got := parseDockerIDFromCgroup(content, nil)
		assert.Equal(t, "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", got)
	})
}

func TestCgroupContainerIDCandidates(t *testing.T) {
	line := "0::/system.slice/docker.service/docker/ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff/init.scope"
	got := cgroupContainerIDCandidates(line)
	assert.Equal(t, []string{"ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"}, got)
}
