# Maintaining Dingzi

[简体中文](MAINTAINING.md) | **English**

Keep deployment to a single binary per component, module boundaries clear, and maintenance costs low. Security comes from explicit permission and resource boundaries. Simplicity comes from removing unnecessary state, branches, and abstractions. See [CONTRIBUTING.en.md](CONTRIBUTING.en.md) for the contribution process and executable commands.

## Module boundaries

| Location | Responsibility and constraints |
| --- | --- |
| `cmd/server`, `cmd/agent` | Arguments/configuration, dependency wiring, startup, and shutdown; business rules belong in the corresponding internal package |
| `internal/server` | HTTP/WS, sessions, storage, traffic, checks, and alerts; split files by responsibility within the package |
| `internal/agent` | Collection, connections, and task execution; use platform files/build tags for platform differences |
| `internal/proto` | Shared messages and protocol constraints; no dependencies on server/agent implementations |
| `internal/server/web` | Embedded plain JS/CSS/HTML; explicit public/admin boundaries, no CDN or frontend build chain |
| `e2e` | Cross-component, deployment, and UI acceptance tests; isolate fixtures from runtime data |

Production dependencies flow from entry points to server/agent to proto. Server and agent do not depend on each other; end-to-end tests may use both. Split files within a package first. Extract a package only when it has an independent, stable responsibility. Avoid catch-all `utils`, `common`, or `base` packages and controller/service/repository layers that merely forward calls.

Define interfaces in the consuming code when there is an actual need. Prefer existing contracts such as `io.Reader`; do not introduce interfaces for hypothetical extensions or mocks.

## File size and splitting

Review responsibilities when a file approaches 300 lines, a function exceeds roughly 60 lines, or control flow nests more than three levels. These are review prompts, not instructions to turn a clear sequence into many jumps. Size does not replace design review: short files can still be tightly coupled.

`python3 scripts/check-structure.py` provides a lightweight CI gate without a lint framework:

| Hand-written files | Line limit | UTF-8 size limit |
| --- | --- | --- |
| Production code, scripts, workflows | 500 | 24 KiB |
| `*_test.go` and `e2e/` tests/fixtures | 700 | 32 KiB |

Line counts include blank lines and comments. Size is measured after normalizing line endings to LF, so Windows and Linux agree. The check covers tracked and non-ignored new Go, JS/MJS, CSS, HTML, Python, Shell, YAML, and Dockerfile sources.

The only excluded directory is the existing third-party `internal/server/web/vendor/`. A generated-file header does not grant an exemption; document the source and regeneration method, then review a precise exclusion. Documentation and lockfiles are outside the source limits. These limits reflect this project's current size; they are neither official Go standards nor quotas to fill.

Existing oversized files are recorded in the [structure baseline](.github/structure-baseline.json). Only metrics already over their normal limit receive an exception:

| File | Current issue | Split before extending the feature |
| --- | --- | --- |
| `internal/server/alerts.go` | Channel/rule APIs, evaluation, and delivery share a file | Separate notification delivery, alert evaluation, and HTTP handling within the package, preserving clear data invariants |
| `internal/server/terminal.go` | Registry, handshakes, and bidirectional forwarding are concentrated | Separate the registry and bridge first; keep locks, session revocation, and close ownership explicit |
| `internal/server/web/app.js` | Dense source combines multiple admin views despite a modest line count | Split machine/check/alert views, with explicit event cleanup and request lifetimes; avoid adding cross-file global state |

The baseline is temporary: oversized metrics must not grow. Lower recorded limits when files shrink and remove exceptions once a metric meets the normal limit. The checker rejects growth, stale limits, and entries for deleted files. Split the relevant responsibility before extending these features, and review remaining entries before each feature release.

Do not bypass the gate by deleting useful comments, compressing lines, or moving production code into tests/vendor. If a limit genuinely needs adjustment, explain why a reasonable split is not possible, the maintenance cost, and the follow-up condition in the PR. A maintainer must review the rule change.

Include relevant behavior regressions when splitting code, and keep every commit buildable. Do not combine function moves with protocol, storage, or error-semantic changes. Keep whole-file formatting separate. When adding frontend files, update embedded routes, template loading order, and browser acceptance checks.

## Security and simplicity

External input includes HTTP/WS messages, agent reports, configuration/database reads, and third-party responses. Boundaries enforce size/range limits and deadlines, and establish validated data representations. Internal functions then work with those contracts. For each check, identify the reachable failure it prevents and who owns the constraint. Check again when data crosses a new trust boundary or concurrent state changes invalidate an assumption.

For example, validate a check's target and timeout when it is created, then use the normalized task in the executor. Execution must still check current permissions/task revisions, handle DNS/network failures, and respond to cancellation. Prior input validation does not make authorization permanent, nor require every layer to repeat the same static range checks.

- Establish invariants during construction/loading. If an internal parameter must be non-nil, state the contract and wire it correctly; do not silently return success from nil fallbacks in every method.
- Handle errors where meaningful action can be taken. Lower layers add useful context and return; the responsible upper layer logs or responds. Use standard-library `%w` and `errors.Is/As`. Avoid duplicate logging, obsolete wrapping dependencies, and panic/recover for ordinary errors.
- Distinguish defaults for missing configuration from corrupt configuration. Initialize only when a new configuration is absent. Parsing, permission, and I/O errors must remain visible; never silently regenerate credentials.
- Bound input parsing and use parameterized SQL. Prefer `textContent` for HTML text and context-appropriate escaping in templates. Public APIs explicitly select public fields instead of serializing structures containing admin data.
- The shared agent secret is for registration only; individual credentials are bound and revoked separately. Session expiration or revocation must close associated terminals. Agents must explicitly enable web terminals. Do not add a general remote `exec` task.
- Proxy headers may influence security decisions only with explicitly trusted proxy configuration. Do not remove cookie protections, access control, request-size limits, or deadlines to reduce branches.

Avoid unbounded retries, silent fallbacks, global recover handlers, and repetitive checks added just in case. Add the smallest clear handling and regression test for a real failure path. Introduce strategies, abstraction layers, or options only when the requirement exists.

## Concurrency, data, and efficiency

Every goroutine, timer, WebSocket connection, and queue needs an owner, an exit condition, and a cleanup responsibility. Prefer synchronous functions; callers that need parallel execution should manage its lifetime. Pass context along the task's call chain and set network deadlines.

Document the state each lock protects and the lock order. Keep long I/O outside shared locks, serialize WebSocket writes, and make cleanup safe to repeat without corrupting state.

Bound memory through fixed-size buffers, bounded queues, task limits, and timeouts. Memory must not grow indefinitely with uptime. Agents report raw counters; the server owns traffic-counter reset handling and billing-cycle calculations. Arithmetic must account for overflow, resets, and UTC month-end boundaries.

Database changes must explain migration, atomicity, retention, and recovery. Backups must be restorable; swapping in an older binary does not necessarily downgrade the data format.

Measure a real path before optimizing and record the workload and baseline. Avoid repeated copies, unbounded reads, sorting while holding locks, and needless allocations. Do not introduce caches, pools, or concurrency for an unmeasured hotspot. Use local variables and straightforward loops when they suffice.

## Review and routine maintenance

Read the full relevant context during review. Check that the change has one purpose, respects module responsibilities, handles failures, releases resources, and tests observable behavior. Security, protocol, storage, or concurrency changes should explain affected invariants and compatibility. Ordinary fixes do not need a separate long design document. Treat personal formatting preferences as suggestions rather than blockers to improvements.

Before adding a dependency, consider the standard library and existing dependencies. Check licensing, maintenance, vulnerabilities, and binary-size/CGO impact. Tidy modules when dependencies change, and avoid unrelated upgrades. Preserve the version/source information and licenses of third-party web assets.

Before each feature release and when updating dependencies/toolchains, review Go security advisories, reachable vulnerabilities, base-image digests, the structure baseline, and test stability. Do not enforce an arbitrary coverage percentage or maintain a sprawling lint rule set. New checks must address an observed problem and produce actionable failures.

Record important decisions with their reasons, boundaries, and verification approach. Remove outdated guidance. Maintain public guides alongside the product and keep Chinese and English versions aligned when commands, defaults, or rules change. Store internal investigations and handoffs separately; do not include chats, host paths, credentials, or audit working papers in public source or build contexts.

## Releases and recovery

Version calculation follows the [contribution guide](CONTRIBUTING.en.md#commit-messages-and-automatic-releases) and the actual workflow. Before publishing, confirm the source, dependencies/licenses, documentation, and artifacts intended for public release. Run CI for the release commit; historical green checks are not evidence for a new change.

1. Verify tests/race checks on all three platforms, installers, nine build targets, and Compose on both architectures. Add browser or migration acceptance checks when the change requires them.
2. The Release workflow passes CI, builds binaries with version/commit metadata, and publishes amd64/arm64 images with an SBOM and provenance. It verifies anonymous pulls by digest before creating the binary release. Check visibility for a new GHCR package separately; do not assume it follows repository visibility.
3. Verify the stable tag, source commit, 18 binaries, and `checksums.txt`. Version output, architectures, and checksums must agree. Image version tags, OCI revision/version, and the digest in the release must match. Update `latest` only for the newest stable release.
4. Record the version, commit, CI links, image digest, verification results, and unverified areas. Do not replace stable tags; ship fixes as new versions.

The Docker workflow can rebuild an existing stable tag. Explain the reason first, then record the new digest, check the version tag/`latest`, and update the image digest in the release notes. Deployments pinned by digest do not move automatically. A mutable tag does not imply a reproducible build.

For installer/database changes, confirm backup and recovery steps first. Preserve diagnostics and data if an upgrade fails. If necessary, roll back the program and restore a compatible backup.

## Sources and further reading

These sources inform the rules; they are not adopted wholesale. Thresholds and workflows reflect this project's scale. Use current Go standard-library APIs in place of obsolete APIs/dependencies from older articles.

- [Go Code Review Comments](https://go.dev/wiki/CodeReviewComments): error handling, context, consumer-owned interfaces, and goroutine lifetimes.
- [Organizing a Go module](https://go.dev/doc/modules/layout) and [Package names](https://go.dev/blog/package-names): `cmd`/`internal` and responsibility-based names.
- [Dave Cheney — Practical Go](https://dave.cheney.net/practical-go/presentations/qcon-china.html): a small number of cohesive packages, usable APIs, and handling each error once.
- [Google — What to look for in a code review](https://google.github.io/eng-practices/review/reviewer/looking-for.html) and [Small CLs](https://google.github.io/eng-practices/review/developer/small-cls.html): understandable changes, related tests, and avoiding speculative generalization.
- [matklad — Push Ifs Up And Fors Down](https://matklad.github.io/2023/11/15/push-ifs-up-and-fors-down.html): centralizing preconditions and control flow to reduce scattered, repeated checks.
