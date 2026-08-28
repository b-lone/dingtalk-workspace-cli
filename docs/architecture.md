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

## Trusted Host HTTP Service

`dwsd` is a Snow-hosted loopback HTTP adapter for Infinity. It preserves the authenticated canonical command and Schema contract, but executes only the six reviewed Infinity read commands through `/Users/yuanzhan/.qoderwork/bin/dws`. The wrapper remains the owner of the QoderWork enterprise session; `dwsd` never reads or persists its access token or enterprise credential headers.

The daemon runs as the Snow login user under the `com.alibaba.dws-http` LaunchAgent and binds to `127.0.0.1:8002`. Its immutable releases, HTTP secrets and logs live under `~/Library/Application Support/DWSService`, outside macOS TCC-protected Documents. Each HTTP request is validated against the embedded `ToolSpec`, converted through a fixed canonical-command-to-CLI mapping and passed as independent argv values without a shell. The request `profile` selects one corpId for that invocation; an omitted profile uses the 0600 default profile file.

Jenkins Job `donut-deploy-dws` is the only deployment owner. It stages an immutable Darwin arm64 release, verifies a candidate on port 8003, then atomically switches the LaunchAgent on port 8002. The previous host release or stopped Docker container is retained only as a deployment rollback point, never as a runtime fallback.

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
