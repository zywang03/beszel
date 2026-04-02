package hub_test

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	beszelTests "github.com/henrygd/beszel/internal/tests"
	pbTests "github.com/pocketbase/pocketbase/tests"
	"github.com/stretchr/testify/require"
)

func TestDockerTransferRoutes(t *testing.T) {
	queueRoot := t.TempDir()
	t.Setenv("BESZEL_HUB_DOCKER_TRANSFER_LOCAL_WRITE", "true")
	t.Setenv("BESZEL_HUB_DOCKER_TRANSFER_QUEUE_ROOT", queueRoot)
	t.Setenv("BESZEL_HUB_DOCKER_TRANSFER_RELAY_HOST", "10.19.131.195")
	t.Setenv("BESZEL_HUB_DOCKER_TRANSFER_RELAY_USER", "luhaotao")
	t.Setenv("BESZEL_HUB_DOCKER_TRANSFER_MAX_IMAGE_SIZE_GB", "120")

	hub, err := beszelTests.NewTestHub(t.TempDir())
	require.NoError(t, err)
	defer hub.Cleanup()

	hub.StartHub()

	adminUser, err := beszelTests.CreateRecord(hub, "users", map[string]any{
		"email":    "admin@example.com",
		"password": "password123",
		"role":     "admin",
	})
	require.NoError(t, err)
	adminToken, err := adminUser.NewAuthToken()
	require.NoError(t, err)

	normalUser, err := beszelTests.CreateRecord(hub, "users", map[string]any{
		"email":    "user@example.com",
		"password": "password123",
		"role":     "user",
	})
	require.NoError(t, err)
	userToken, err := normalUser.NewAuthToken()
	require.NoError(t, err)

	statusTaskID := "status-task-001"
	require.NoError(t, os.MkdirAll(filepath.Join(queueRoot, "tasks", "running"), 0755))
	require.NoError(t, os.MkdirAll(filepath.Join(queueRoot, "logs"), 0755))
	require.NoError(
		t,
		os.WriteFile(
			filepath.Join(queueRoot, "tasks", "running", statusTaskID+".json"),
			[]byte(`{
  "id": "status-task-001",
  "source_node": "xsuper@10.15.88.84",
  "dest_node": "xsuper@10.15.88.85",
  "container_name": "demo-container",
  "target_image": "demo:migrated",
  "target_container": "demo-copy",
  "setup_volume": true,
  "mount_bindings": [
    {
      "inner_path": "/data",
      "dest_vol_dir": "/srv/demo/data"
    }
  ],
  "auto_start": true,
  "port": "22315",
  "source_sudo_password": "should-not-leak",
  "dest_sudo_password": "should-not-leak",
  "progress": {
    "state": "running",
    "stage": "同步中转机缓存到目标服务器",
    "message": "正在把中转机缓存推送到目标服务器",
    "updated_at": "2026-04-03T01:23:45Z",
    "history": [
      {
        "at": "2026-04-03T01:23:45Z",
        "level": "info",
        "state": "running",
        "stage": "同步中转机缓存到目标服务器",
        "message": "正在把中转机缓存推送到目标服务器"
      }
    ]
  }
}`),
			0644,
		),
	)
	require.NoError(
		t,
		os.WriteFile(
			filepath.Join(queueRoot, "logs", statusTaskID+".log"),
			[]byte("line-1\nline-2\n目标端口已被占用: 22315\n"),
			0644,
		),
	)

	testAppFactory := func(t testing.TB) *pbTests.TestApp {
		return hub.TestApp
	}

	scenarios := []beszelTests.ApiScenario{
		{
			Name:   "GET /docker-transfer/config - with admin auth should succeed",
			Method: http.MethodGet,
			URL:    "/api/beszel/docker-transfer/config",
			Headers: map[string]string{
				"Authorization": adminToken,
			},
			ExpectedStatus:  200,
			ExpectedContent: []string{`"enabled":true`, queueRoot, "10.19.131.195", "luhaotao", `"maxImageSizeGb":120`},
			TestAppFactory:  testAppFactory,
		},
		{
			Name:   "POST /docker-transfer/tasks - with user auth should fail",
			Method: http.MethodPost,
			URL:    "/api/beszel/docker-transfer/tasks",
			Headers: map[string]string{
				"Authorization": userToken,
			},
			Body: jsonReader(map[string]any{
				"sourceUser":    "xsuper",
				"sourceNode":    "10.15.88.84",
				"destUser":      "xsuper",
				"destNode":      "10.15.88.85",
				"containerName": "demo",
				"targetImage":   "demo:migrated",
			}),
			ExpectedStatus:  403,
			ExpectedContent: []string{"Requires admin role"},
			TestAppFactory:  testAppFactory,
		},
		{
			Name:   "POST /docker-transfer/tasks - with admin auth should create queue file",
			Method: http.MethodPost,
			URL:    "/api/beszel/docker-transfer/tasks",
			Headers: map[string]string{
				"Authorization": adminToken,
			},
			Body: jsonReader(map[string]any{
				"sourceSystemId":            "src-system-id",
				"sourceSystemName":          "源服务器",
				"destSystemId":              "dst-system-id",
				"destSystemName":            "目标服务器",
				"sourceUser":                "vihuman",
				"sourceNode":                "10.15.88.84",
				"destUser":                  "vihuman",
				"destNode":                  "10.15.88.85",
				"useJumpHost":               true,
				"containerName":             "rlinf-v1.1-qianyx",
				"targetImage":               "rlinf:v1.1-migrated",
				"allowTargetImageOverwrite": true,
				"setupVolume":               true,
				"mountBindings": []map[string]any{
					{
						"innerPath":  "/data",
						"destVolDir": "/srv/rlinf/data",
					},
					{
						"innerPath":  "/cache",
						"destVolDir": "/srv/rlinf/cache",
					},
				},
				"autoStart":               true,
				"targetContainer":         "rlinf-copy",
				"extraRunArgs":            "--ipc=host -e HF_HOME=/data/hf",
				"sourceSudoPassword":      "source-pass-123",
				"destSudoPassword":        "dest-pass-456",
				"removeExistingContainer": true,
				"keepLocalCache":          true,
				"shmSize":                 "512G",
			}),
			ExpectedStatus:  200,
			ExpectedContent: []string{`"success":true`, `"taskId":`, queueRoot},
			TestAppFactory:  testAppFactory,
			AfterTestFunc: func(t testing.TB, _ *pbTests.TestApp, _ *http.Response) {
				files, err := filepath.Glob(filepath.Join(queueRoot, "tasks", "pending", "*.json"))
				require.NoError(t, err)
				require.Len(t, files, 1)

				data, err := os.ReadFile(files[0])
				require.NoError(t, err)

				task := map[string]any{}
				require.NoError(t, json.Unmarshal(data, &task))
				require.Equal(t, "vihuman@10.15.88.84", task["source_node"])
				require.Equal(t, "vihuman@10.15.88.85", task["dest_node"])
				require.Equal(t, true, task["use_jump_host"])
				require.Equal(t, "rlinf-v1.1-qianyx", task["container_name"])
				require.Equal(t, "rlinf:v1.1-migrated", task["target_image"])
				require.Equal(t, "admin@example.com", task["requested_by"])
				require.Equal(t, true, task["allow_target_image_overwrite"])
				require.Equal(t, true, task["setup_volume"])
				require.Equal(t, true, task["auto_start"])
				require.Equal(t, "/data", task["inner_path"])
				require.Equal(t, "/srv/rlinf/data", task["dest_vol_dir"])
				require.Equal(t, "rlinf-copy", task["target_container"])
				require.Equal(t, "--ipc=host -e HF_HOME=/data/hf", task["extra_run_args"])
				require.Equal(t, "source-pass-123", task["source_sudo_password"])
				require.Equal(t, "dest-pass-456", task["dest_sudo_password"])
				require.Equal(t, "512G", task["shm_size"])
				require.Equal(t, float64(120), task["max_image_size_gb"])

				mountBindings, ok := task["mount_bindings"].([]any)
				require.True(t, ok)
				require.Len(t, mountBindings, 2)

				firstBinding, ok := mountBindings[0].(map[string]any)
				require.True(t, ok)
				require.Equal(t, "/data", firstBinding["inner_path"])
				require.Equal(t, "/srv/rlinf/data", firstBinding["dest_vol_dir"])

				secondBinding, ok := mountBindings[1].(map[string]any)
				require.True(t, ok)
				require.Equal(t, "/cache", secondBinding["inner_path"])
				require.Equal(t, "/srv/rlinf/cache", secondBinding["dest_vol_dir"])
			},
		},
		{
			Name:   "POST /docker-transfer/tasks - with conflicting port and extraRunArgs should fail",
			Method: http.MethodPost,
			URL:    "/api/beszel/docker-transfer/tasks",
			Headers: map[string]string{
				"Authorization": adminToken,
			},
			Body: jsonReader(map[string]any{
				"sourceUser":    "vihuman",
				"sourceNode":    "10.15.88.84",
				"destUser":      "vihuman",
				"destNode":      "10.15.88.85",
				"containerName": "demo",
				"targetImage":   "demo:migrated",
				"autoStart":     true,
				"port":          "22315",
				"extraRunArgs":  "--entrypoint /usr/sbin/sshd",
			}),
			ExpectedStatus:  400,
			ExpectedContent: []string{"ExtraRunArgs cannot contain --entrypoint or sshd when startup port is set"},
			TestAppFactory:  testAppFactory,
		},
		{
			Name:   "GET /docker-transfer/task-status - with admin auth should return sanitized progress and logs",
			Method: http.MethodGet,
			URL:    "/api/beszel/docker-transfer/task-status?taskId=" + statusTaskID,
			Headers: map[string]string{
				"Authorization": adminToken,
			},
			ExpectedStatus: 200,
			ExpectedContent: []string{
				`"found":true`,
				`"queueState":"running"`,
				`"stage":"同步中转机缓存到目标服务器"`,
				`"setupVolume":true`,
				`"autoStart":true`,
				`"port":"22315"`,
				`"mountBindings":[{"destVolDir":"/srv/demo/data","innerPath":"/data"}]`,
				"目标端口已被占用: 22315",
			},
			NotExpectedContent: []string{
				"should-not-leak",
			},
			TestAppFactory: testAppFactory,
		},
	}

	for _, scenario := range scenarios {
		scenario.Test(t)
	}
}
