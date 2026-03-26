# Build Commands Summary

This file summarizes the Docker build and packaging commands currently used in this repo.

## Working Directory

Run all commands from the repo root:

```bash
cd /data/gpu_monitor/beszel
```

## 1. Build Hub Image

```bash
docker build -f internal/dockerfile_hub \
  -t zywang03/beszel-hub:hub-v1 .
```

## 2. Build Agent Images

### Default Agent

```bash
docker build -f internal/dockerfile_agent \
  -t zywang03/beszel-agent:agent-v1 .
```

### Alpine Agent

```bash
docker build -f internal/dockerfile_agent_alpine \
  -t zywang03/beszel-agent:alpine-v1 .
```

### Intel Agent

```bash
docker build -f internal/dockerfile_agent_intel \
  -t zywang03/beszel-agent-intel:intel-v1 .
```

### NVIDIA Agent

```bash
docker build -f internal/dockerfile_agent_nvidia \
  -t zywang03/beszel-agent-nvidia:gpu-v1 .
```

## 3. Check Built Images

```bash
docker images | grep beszel
```

## 4. Push Images to Docker Hub

```bash
docker login
docker push zywang03/beszel-hub:hub-v1
docker push zywang03/beszel-agent:agent-v1
docker push zywang03/beszel-agent:alpine-v1
docker push zywang03/beszel-agent-intel:intel-v1
docker push zywang03/beszel-agent-nvidia:gpu-v1
```

## 5. Export Images as Offline Packages

```bash
docker save zywang03/beszel-hub:hub-v1 | gzip > beszel-hub-hub-v1.tar.gz
docker save zywang03/beszel-agent:agent-v1 | gzip > beszel-agent-agent-v1.tar.gz
docker save zywang03/beszel-agent:alpine-v1 | gzip > beszel-agent-alpine-v1.tar.gz
docker save zywang03/beszel-agent-intel:intel-v1 | gzip > beszel-agent-intel-v1.tar.gz
docker save zywang03/beszel-agent-nvidia:gpu-v1 | gzip > beszel-agent-nvidia-gpu-v1.tar.gz
```

## 6. Load Offline Packages

```bash
gunzip -c beszel-hub-hub-v1.tar.gz | docker load
gunzip -c beszel-agent-agent-v1.tar.gz | docker load
gunzip -c beszel-agent-alpine-v1.tar.gz | docker load
gunzip -c beszel-agent-intel-v1.tar.gz | docker load
gunzip -c beszel-agent-nvidia-gpu-v1.tar.gz | docker load
```

## 7. Run Hub Locally

```bash
docker run -d \
  --name beszel-hub \
  --restart unless-stopped \
  -p 8090:8090 \
  -v $(pwd)/beszel_data:/beszel_data \
  zywang03/beszel-hub:hub-v1
```

## Notes

- `hub` uses `internal/dockerfile_hub`.
- Default agent uses `internal/dockerfile_agent`.
- Alpine agent uses `internal/dockerfile_agent_alpine`.
- Intel agent uses `internal/dockerfile_agent_intel`.
- NVIDIA agent uses `internal/dockerfile_agent_nvidia`.
- The final `.` in `docker build` is required because the Dockerfiles copy files from the repo root build context.
