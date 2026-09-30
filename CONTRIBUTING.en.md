# Contributing

[简体中文](CONTRIBUTING.md) | **English**

Dingzi is a lightweight server monitoring panel and agent written in Go. Web assets are embedded, with no frontend build step. Contributions should make the project easier to use and maintain. See the [maintenance guide](MAINTAINING.en.md) for project boundaries and design conventions.

## Issues and changes

Bug reports should include the version, operating system and architecture, deployment method, reproduction steps, expected and actual results, and redacted logs. Do not upload runtime configuration, databases, passwords, agent credentials, or OAuth secrets.

For a new dependency, protocol/storage change, or feature spanning modules, briefly describe the problem, proposed approach, and compatibility implications in an issue or PR first. Small fixes can go straight to a PR.

Branch from `main` and target `main` with your PR. Each PR should solve one independently verifiable problem, including related tests and necessary documentation. Separate broad formatting, file moves, and behavior changes so reviewers can assess each. Describe the user-visible change, verification commands and environment, and any unverified areas. For data or deployment changes, explain upgrades and recovery.

## Development and verification

Use the Go toolchain specified by CI (currently 1.27.1; see `go.mod` for the minimum language version), Node.js 22+, Python 3.10+, and Git. Run the following only in an environment where builds are allowed. If local testing is restricted, use an authorized remote CI environment and record its results.

Examples use `python3`; on Windows, `python` is also suitable. Run all commands from the repository root.

```sh
go build ./...
go vet ./...
go test -count=1 -timeout 10m ./...
go test -race -count=1 -timeout 10m ./...
gofmt -l .
node --test e2e/board_test.mjs
python3 scripts/check-structure.py
python3 -B -m unittest discover -s e2e -p structure_test.py
sh -n install.sh
sh -n install-server.sh
sh -n e2e/service_install.sh
```

`gofmt -l .` should print nothing. If it reports files, format only the Go files involved in your change. For JS changes, also run `node --check <file>`.

The race detector requires a C compiler; release binaries are still built with `CGO_ENABLED=0`. Installer tests are skipped on Windows when no POSIX `sh` is available. Real PTY tests run only on Linux/macOS. Record skipped tests as skipped, not passed.

Choose verification appropriate to the change; CI runs the full matrix before completion:

| Change | Required evidence |
| --- | --- |
| Go logic | Tests for affected packages; race checks for concurrency changes; reproducible benchmarks/profiles for performance work |
| Sessions, permissions, credentials | Behavior regressions for success, rejection, expiration, and revocation |
| Protocol, quotas, storage | Compatibility, boundary values, restart/persistence behavior; failures must not appear successful |
| UI | Node state regressions; browser checks for interaction/layout changes, including desktop, mobile, empty, and error states |
| Installers | Go installer regressions and real systemd/OpenRC CI checks for installation, upgrades, and data retention after removal |
| Containers | Image build and Compose smoke tests; CI runs on native amd64 and arm64 hosts |
| Documentation/formatting | Valid links and examples consistent with workflows; no new tests needed for wording changes alone |

CI includes Linux, Windows, and macOS build/vet/test/race jobs, Node state regressions, formatting and structure checks, real systemd/OpenRC checks, nine cross-compilation targets, and container checks on both architectures. Browser acceptance tests currently run manually.

### Browser acceptance tests

Tests use a temporary panel, a real local agent, and a local notification receiver. They need no production credentials or real OAuth App. Prepare binaries, Playwright, and a browser in an environment where builds are allowed; the example below uses a POSIX shell:

```sh
mkdir -p e2e/run/tools
go build -o e2e/run/dingzi-server ./cmd/server
go build -o e2e/run/dingzi-agent ./cmd/agent
npm install --prefix e2e/run/tools --no-save --package-lock=false playwright@1.56.1
node e2e/run/tools/node_modules/playwright/cli.js install chromium
export DINGZI_BIN="$PWD/e2e/run"
export DINGZI_TOOLS="$PWD/e2e/run/tools"
export DINGZI_REVIEW="$PWD/e2e/run/review"
node e2e/browser.mjs
node e2e/github-browser.mjs
```

On Windows, add `.exe` to binary names and set the same environment variables through PowerShell's `$env:NAME` syntax. The scripts use the system Chrome installation by default on Windows; set `DINGZI_CHROME` for another location. Keep screenshots and dependencies under the ignored `e2e/run` directory. Do not commit test output.

### Container and service checks

```sh
docker build --build-arg VERSION=ci -t dingzi-ci:local .
python3 e2e/docker_smoke.py
```

This requires a Linux container engine and Docker Compose v2. The smoke test uses an isolated project name, checks health, login, non-root/read-only permissions, persistence, and clean shutdown, and cleans up its own resources.

`e2e/service_install.sh` changes system services and system directories. Run it only as root in a disposable Linux environment with `DINGZI_SERVICE_TEST=1` explicitly set. Use the Go installer tests for routine development and CI for real service acceptance checks.

## Code and test conventions

- Organize files by responsibility. Do not keep adding features to entry points, route registries, or existing large files. Check the maintenance guide's limits and planned splits first.
- Use `gofmt`, early error returns, and names that express purpose. Prefer concrete types and the standard library; do not introduce speculative frameworks, factories, or interfaces just for mocking.
- Keep JS/CSS readable and expand control flow. Do not hand-minify source to make files look shorter; format only the relevant scope.
- Parse, validate, and normalize external input at entry points. Internal code should use the established types and invariants. Avoid repeating nil/range checks at every layer or hiding errors behind empty values.
- Test observable behavior and real failure boundaries, not private implementation details. Do not rely on tiny sleeps or filesystem timestamp precision to establish ordering. Use synchronization/events or waits with deadlines, and set fixture times explicitly.
- Comments should explain reasoning, protocol constraints, or resource ownership. Avoid restating code or leaving TODOs without ownership and a follow-up condition.

## Commit messages and automatic releases

Use Conventional Commits: `<type>(optional scope): <description>`. Squash merges are recommended, so the PR title should work as the resulting commit title. The Release workflow on `main` examines commits since the last stable release and decides whether to publish after the full CI gate passes.

| Type | Version change |
| --- | --- |
| `feat` | Minor, for example 0.1.0 → 0.2.0 |
| `fix`, `perf`, `refactor`, `revert` | Patch |
| `docs`, `chore`, `ci`, `test`, `style`, `build` | Do not trigger a release on their own |
| A `!` after the type/scope, or `BREAKING CHANGE:` in the body | Major, including during 0.x development |

Use an accurate `fix` commit for dependency security fixes that need delivery to users; labeling them `build` alone would omit a release. Same-repository PRs can receive rolling prereleases after checks pass. Fork PRs receive Actions build artifacts only, with no permission to publish releases or images.

When trying a prerelease, use matching versions of the panel, agent, and installer scripts. Published stable tags remain unchanged.
