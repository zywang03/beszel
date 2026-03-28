# Repository Guidelines

## Project Structure & Module Organization
`internal/cmd/hub` and `internal/cmd/agent` are the main entrypoints. Core hub logic lives in `internal/hub`, alerting in `internal/alerts`, shared domain models in `internal/entities`, and supporting helpers in `internal/common`, `internal/records`, and `internal/users`. Host-side collectors and OS-specific implementations live in `agent/` with patterns like `gpu_amd_linux.go` and `systemd_nonlinux.go`. The web UI is in `internal/site` (`src/` for React code, `public/` for static assets, `dist/` for build output). Reusable hub/API test scaffolding is in `internal/tests`. Deployment examples live under `supplemental/docker` and `supplemental/kubernetes`.

## Build, Test, and Development Commands
Use Go 1.26.x. Common targets:

- `make build`: build both hub and agent binaries into `build/`.
- `make build-hub` / `make build-agent`: build one component only.
- `make test`: run Go tests with the repository’s `testing` build tag.
- `make lint`: run `golangci-lint`.
- `make dev`: start the Vite UI, development hub, and agent together.
- `make dev-server`, `make dev-hub`, `make dev-agent`: run one development process.

For frontend-only work, use Bun if available, otherwise npm: `bun run --cwd internal/site check` or `npm run --prefix internal/site check`.

## Coding Style & Naming Conventions
Follow standard Go formatting with `gofmt`; keep packages focused and file names lower_snake_case. Use suffixes to signal platform/build scope (`*_linux.go`, `*_windows.go`, `*_test.go`). In `internal/site`, Biome enforces tabs, double quotes, no semicolons, and a 120-column line width. React component files use kebab-case (`systems-table.tsx`) while exported component names stay PascalCase. If UI text changes, regenerate locales with `make generate-locales` or `npm run --prefix internal/site sync`.

## Testing Guidelines
Tests live beside the code they cover and use Go’s `testing` package, often with `testify`. Name files `*_test.go`; shared setup belongs in `*_test_helpers.go` or `internal/tests`. Prefer targeted package runs while iterating, then finish with `make test`.

## Commit & Pull Request Guidelines
Recent history favors short, imperative subjects with optional scopes, for example `fix(agent): ...`, `feat(hub): ...`, and `refactor(hub): ...`. Keep commits focused. PRs should follow `.github/pull_request_template.md`: add a clear description, note any docs PR, summarize changes by category, and include screenshots for UI changes.
