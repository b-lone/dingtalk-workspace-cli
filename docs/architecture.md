# Architecture

`dws` is a Go CLI with a versioned, static command surface for DingTalk MCP capabilities. Cobra help serves humans; the embedded Command Catalog serves AI agents.

## High-Level Flow

1. `cmd` is the CLI entrypoint, invoking `internal/app` to build the root Cobra command tree.
2. `internal/app` wires static utility commands (`auth`, `audit`, `schema`, `completion`), product helpers, and versioned plugin descriptors.
3. `internal/helpers` contains the main command handlers for all product surfaces (`dev`, `chat`, `calendar`, `contact`, `aitable`, etc.).
4. `internal/executor` and `internal/transport` execute MCP JSON-RPC calls; `internal/output` formats responses.
5. `internal/auth` manages login state, PAT tokens, and agent-code detection.
6. Schema generation starts from the reviewed `CommandRegistry`, binds each identity to the exact current Cobra leaf, and then resolves typed constraints, sanitized MCP snapshots, Agent hints, and Skills into one `SchemaRegistry`. Startup and Schema queries do not call MCP `tools/list`.
7. The embedded Catalog is a downstream release artifact and never backfills identity or participates in regeneration. Stable flag-to-interface property bindings come from the reviewed, content-addressed v3 manifest in `schema_parameter_bindings.json`; its exact active tuples, corrections, removals, and mapping exclusions are validated against the final bound `SchemaRegistry`. CLI `required` and constraints come from the resolved typed contract, while MCP `required` remains interface-only metadata.
8. Agent selection results are fixed in versioned review inputs. Every public tool has explicit use/avoid/example and interface disposition metadata; Skill references that are not current leaves require an explicit alias/group/stale/out-of-surface review instead of fuzzy runtime matching.

## Self-Contained Docker HTTP Service

`dwsd` is a Snow-hosted HTTP adapter for Infinity. It runs the DWS Core in-process and exposes only the six reviewed Infinity read commands. It does not invoke a QoderWork or QwenWork binary and does not depend on either desktop process, installation directory, login lifecycle, or update lifecycle.

The daemon runs as the non-root `dws` user in the `dws` Docker Compose service. The Linux arm64 image contains the same-revision `dwsd` daemon and `dws` management CLI, is published by immutable digest, and carries the exact Git revision in `org.opencontainers.image.revision`. Snow port 8002 maps to container port 8080 for the Infinity caller.

Service-owned state is isolated from desktop products:

- `/Users/yuanzhan/Documents/Data/dws-service/state` is mounted at `/var/lib/dws` and owns the profile registry, configuration, encrypted credentials, and Linux file-DEK.
- `/Users/yuanzhan/Documents/Data/dws-service/secrets` is mounted read-only at `/run/secrets` and owns only the HTTP Bearer and default profile selector.
- `DWS_CHANNEL` is a required Compose input and is used by login and every business request.
- `DINGTALK_DWS_AGENTCODE` is the fixed DWSService identity.

Each HTTP request is validated against the embedded `ToolSpec`. The request `profile` selects one registered corpId for that invocation; an omitted profile uses the 0600 default profile file. Profile selection and credential refresh remain serialized inside the process. There is no wrapper, shell, generic argv boundary, LaunchAgent, or desktop-product fallback.

Jenkins Job `donut-deploy-dws` is the only deployment owner. It checks out one exact Git revision, builds and publishes the Linux arm64 image, resolves its registry digest, and deploys that digest through Docker Compose. `scripts/deploy-dws-docker.sh` makes the registered channel explicit, requires `/healthz`, `/readyz`, the six-command Schema, the selected organization identity, and the target group query to pass, and restores the previous successful Docker image when post-deployment business verification fails. A successful release leaves exactly one Docker runtime on port 8002; host binaries and LaunchAgent state are not runtime or rollback dependencies.

## Repository Structure

- `cmd`: CLI entrypoint
- `internal/app`: root command wiring, static utility commands, and plugin loading
- `internal/helpers`: product command handlers (dev, chat, calendar, contact, etc.)
- `internal/plugin`: versioned plugin manifest, hook, skill, and transport descriptor loading
- `internal/cli`: embedded Agent Command Catalog, static schema query, and catalog contracts
- `internal/generator`: deterministic Agent metadata and Command Catalog generators
- `internal/executor`: invocation dispatch and result handling
- `internal/transport`: MCP HTTP client and request signing
- `internal/auth`: login, token management, agent-code detection, identity
- `internal/audit`: user operation audit log (JSONL, hash chain, forwarding)
- `internal/errors`: structured error model with categories and hints
- `internal/keychain`: OS keychain integration for credential storage
- `internal/security`: endpoint allowlist and domain trust
- `internal/safety`: runtime safety checks (confirm prompts, dry-run guards)
- `internal/cobracmd`: shared Cobra command builders
- `internal/pat`: PAT (Personal Access Token) authorization flow
- `internal/output`: response formatting (json, table, raw, pretty)
- `internal/logging`: structured logging and argument sanitization
- `internal/tui`: terminal UI helpers
- `internal/recovery`: panic recovery and graceful degradation
- `pkg/configmeta`: environment variable registry and documentation
- `pkg/config`: configuration constants and paths
- `pkg/edition`: edition detection (oss vs enterprise)
- `pkg/mcptypes`: MCP protocol type definitions
- `internal/syncdata`: generated static endpoint and command-routing data synced from the Wukong baseline
- `skills/`: bundled agent skills (mono/ and multi/ layouts)
- `test/`: CLI, integration, contract, unit, and skill E2E tests
- `scripts/`: install scripts, policy checks, and CI helpers
