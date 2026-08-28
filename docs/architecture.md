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

## Self-Contained Host HTTP Service

`dwsd` is a Snow-hosted loopback HTTP adapter for Infinity. It runs the DWS Core in-process and exposes only the six reviewed Infinity read commands. It does not invoke a QoderWork or QwenWork binary and does not depend on either desktop process, installation directory, login lifecycle, or update lifecycle.

The daemon runs as the Snow login user under the `com.alibaba.dws-http` LaunchAgent and binds to `127.0.0.1:8002`. Immutable releases live under `~/Library/Application Support/DWSService/releases/<git-sha>` and contain both `dwsd` and the same-revision `dws` management CLI. The `current` symlink selects one complete release atomically.

Service-owned state is isolated from desktop products:

- `DWSService/state/config` owns the profile registry and DWS configuration.
- `DWSService/state/keychain` owns the encrypted credential files; the macOS Keychain remains the DEK backend.
- `DWSService/secrets` owns only the HTTP Bearer and default profile selector.
- `DINGTALK_DWS_AGENTCODE` is an explicit deployment input for the DWSService identity.

Each HTTP request is validated against the embedded `ToolSpec`. The request `profile` selects one registered corpId for that invocation; an omitted profile uses the 0600 default profile file. Profile selection and credential refresh remain serialized inside the process. There is no wrapper, shell, generic argv boundary, or desktop-product fallback.

The bundled CLI is used only for explicit service authentication and maintenance:

```bash
scripts/dws-service-auth.sh \
  --agent-code dws-http-service \
  login --device
```

The first deployment must initialize the service-owned login state before candidate startup. Use the same-revision CLI build explicitly when `current/dws` does not exist yet, then select the target organization in the device authorization page:

```bash
DWS_SERVICE_CLI_BINARY=/absolute/path/to/dws \
scripts/dws-service-auth.sh \
  --agent-code dws-http-service \
  login --device
```

After the first login, deployment uses the returned corpId as `--profile`. A later re-authorization may pass that registered corpId to `dws auth login --profile` explicitly.

Jenkins Job `donut-deploy-dws` is the only deployment owner. It builds `dwsd` and `dws` from the same Git revision, stages one immutable release, verifies a candidate on port 8003, then atomically switches the LaunchAgent on port 8002. Only the previous immutable host release is eligible for rollback; Docker and desktop-product binaries are not runtime or rollback dependencies.

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
