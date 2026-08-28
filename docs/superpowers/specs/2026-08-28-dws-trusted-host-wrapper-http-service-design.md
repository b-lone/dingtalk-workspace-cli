# DWS 受信任主机 Wrapper HTTP 服务设计

日期：2026-08-28

状态：方向已确认，待文档复核后实施

## 结论

将 Snow 上的 DWS HTTP 服务从“Docker 内直接运行开源 DWS Core”调整为“Snow 登录用户下的 LaunchAgent 调用受信任 `~/.qoderwork/bin/dws` wrapper”。HTTP Bearer、canonical command、Schema 和 `profile` 协议保持不变；企业凭据继续由 QoderWork/DWS 本机认证状态持有，不复制到服务目录、代码、URL 或容器。

Infinity 对所有 DWS 请求显式发送其已配置的 `dws.corp_id` 作为 `profile`。Alibaba 使用已验证的 corpId `dingd8e1123006514592`，不从钉钉应用的 `org_id`、Client ID、Robot Code 或群 ID 推断企业身份。

## 背景与已验证事实

1. Snow 主机上的 `/Users/yuanzhan/.qoderwork/bin/dws` 能以 Alibaba profile 查询群基础信息，并返回群名“构建通知”。
2. 当前 Docker 服务的开源 Core 即使存在 Alibaba token，也会在企业 MCP 调用时返回 `ENTERPRISE_NOT_AUTHORIZED`；该运行路径不具备 QoderWork 私有 edition 的企业身份注入能力。
3. 受信任 wrapper 通过本机 QoderWork 会话完成企业身份注入。凭据事实源是 Snow 登录用户的 QoderWork/DWS 认证状态，不是 Docker state、`profiles.json` 或 Infinity。
4. Infinity 当前只依赖六个只读 DWS canonical command，并通过固定版本、catalog hash、surface hash 和 leaf Schema 校验服务契约。
5. Infinity 的 `dws.corp_id` 已承担启动身份校验中的期望企业 corpId，适合作为同一运行时的显式 profile；应用 `org_id` 只是 Infinity 的业务组织键，不能替代 DWS profile。

## 目标

- 让 Snow 上的 DWS HTTP 服务使用已经登录且可访问 Alibaba 企业能力的受信任 wrapper。
- 保留现有 HTTP 调用方契约和失败分类，不让 Infinity 接触本机企业凭据。
- 支持请求级 `profile`，且默认 profile、启动身份校验和业务命令具有一致语义。
- 仅发布 Infinity 已登记的六个只读命令，避免把通用本机 CLI 暴露成远程 argv 执行器。
- 通过现有唯一 Jenkins 发布所有者完成构建、切换、健康检查和回滚。

## 非目标

- 不把 QoderWork 私有 edition 的身份头逻辑复制到开源 Core。
- 不迁移、导出或重新加密现有 QoderWork/DWS 凭据。
- 不支持调用方提交任意 CLI path、argv、环境变量、工作目录或可执行文件路径。
- 不为应用 `org_id` 建立猜测式 corpId 映射。
- 不保留 Docker Core 作为运行时 fallback；Docker 只作为切换失败时的已知旧版本回滚对象。
- 不扩大到写操作或需要用户确认的命令。

## 总体架构

```text
Infinity
  |  Bearer + canonical command + arguments + profile
  v
dwsd (Snow LaunchAgent, 127.0.0.1:8002)
  |  HTTP 鉴权 / Schema / 参数校验 / 只读 allowlist
  v
TrustedHostRunner
  |  固定 wrapper 路径 + 固定命令映射 + 独立 argv
  v
~/.qoderwork/bin/dws
  |  本机 QoderWork 会话与企业身份注入
  v
DingTalk MCP
```

唯一凭据边界如下：

- HTTP 服务只读取现有 0600 Bearer 文件和默认 profile 文件。
- wrapper 自行访问 Snow 登录用户的 QoderWork/DWS 认证状态。
- `dwsd` 不读取、不缓存、不返回 access token、refresh token 或企业凭据头。
- LaunchAgent plist 只保存文件路径和非敏感运行参数，不保存密钥值。

## HTTP 契约

现有接口保持不变：

- `GET /healthz`
- `GET /readyz`
- `GET /v1/schema`
- `GET /v1/schema/{canonical_path}`
- `POST /v1/commands/{canonical_path}/execute`

执行请求继续使用：

```json
{
  "profile": "dingd8e1123006514592",
  "arguments": {
    "group": "cidhi+YfuQpv/ufPpIsyrmkug=="
  }
}
```

约束：

- Bearer 校验继续使用常量时间比较。
- `profile` 为空时使用 0600 profile 文件中的默认 corpId。
- `profile` 非空时作为单次命令的 profile selector；禁止空白、控制字符、CSV 多 profile 和超长值。
- 服务始终使用 `--profile=<selector>` 的单个 argv 元素，避免 selector 被解释成其他 flag。
- `arguments` 仍先依据嵌入式 ToolSpec 完成未知字段、必填字段、类型、默认值和安全属性校验。
- HTTP 请求永远不能直接影响 wrapper 路径、CLI path、环境变量或原始 argv。

## 命令范围与参数编码

TrustedHostRunner 只实现 Infinity 当前固定注册的六个只读命令：

| Canonical command | Wrapper CLI path |
| --- | --- |
| `contact.get_current_user_profile` | `contact user get-self` |
| `contact.search_contact_by_key_word` | `contact user search` |
| `chat.get_conversation_info` | `chat conversation-info` |
| `calendar.list_calendars` | `calendar book list` |
| `calendar.list_calendar_events` | `calendar event list` |
| `todo.get_user_todos_in_current_org` | `todo task list` |

每条命令使用代码内显式、可审查的 canonical path 到 CLI path/flag 编码映射。不能按字符串拆分调用方输入生成命令，也不能把未知属性透传给 wrapper。

编码规则：

- 字符串、整数和布尔值分别编码为单独的 flag value；布尔文本只允许 `true`/`false`。
- 数组按对应 CLI 的 CSV 约定编码，数组成员先做类型和空值校验。
- Unix 毫秒时间转换为等价的 UTC RFC3339 字符串，再交给 CLI 的既有时间解析逻辑。
- 所有 CLI path 和 flag 名来自固定映射，所有值通过 `exec.Cmd.Args` 独立传递；不经过 shell。
- wrapper 固定附加 `--format=json` 和有上限的 `--timeout=<seconds>`。

## 进程执行与输出边界

新增 `internal/hostcli` 模块，职责如下：

1. 校验 wrapper 是绝对路径、普通可执行文件，且不是 group/other writable。
2. 以受控环境启动 wrapper，只继承运行所需的 `HOME`、`USER`、`LOGNAME`、`TMPDIR`、`PATH` 和 locale；不注入任何 token 或企业身份头。
3. 为每次调用创建独立进程组；超时或服务关闭时终止整个进程组，避免遗留 shim 子进程。
4. 分别限制 stdout 和 stderr 大小。stdout 超限返回现有 output-limit 分类；stderr 只保留有界诊断，不原样返回给调用方。
5. stdout 必须是单个 JSON 对象。成功对象进入现有 result normalizer；非 JSON、空输出或结构不合法按上游契约错误处理。
6. 优先解析 wrapper 的结构化错误分类；无法解析的非零退出统一映射为稳定的 upstream 错误，不向客户端泄露本机路径、命令行或 stderr。

`commandservice.Service` 继续负责：

- canonical command 解析；
- ToolSpec 参数校验和默认值；
- safety、confirmation、dry-run 约束；
- 串行化 profile 相关执行；
- Schema 和 metadata 输出；
- 结果归一化。

旧的 `NewHTTPCommandService` 直接 Core 执行路径从 `dwsd` 移除，不做双路径 fallback。普通交互式 `dws` CLI 不受影响。

## Readiness 与身份一致性

`dwsd` 启动时必须完成以下检查后才进入 ready：

1. 配置文件权限、wrapper 路径和监听地址合法。
2. 默认 profile 非空且是单个 selector。
3. 通过 wrapper 对默认 profile 执行 `contact user get-self`。
4. 响应声明成功，并包含非空 `corpId` 和 `userId`。
5. 返回的 `corpId` 与默认 profile corpId 一致。

服务缓存已验证的默认身份供 `/readyz` 使用；命令出现认证、身份不一致或 wrapper 会话不可用时将 readiness 置为失败。后续 readiness 探测通过同一只读身份命令恢复，不通过重启、token 复制或 Core fallback 修复。

Infinity 启动时仍执行既有版本、hash、六个 leaf Schema 和当前用户校验。不同之处是所有执行请求（包括当前用户校验）都显式发送 `dws.corp_id` 作为 `profile`，因此启动校验和业务命令不会落到不同企业身份。

## Infinity 改动

Infinity 的 DWS HTTP executor 在请求体中增加：

```python
{
    "profile": self._settings.corp_id,
    "arguments": arguments,
}
```

适用于启动身份探测和全部六个业务命令，不在钉钉应用路由中单独拼 profile。这样 profile 的唯一事实源仍是 DWS runtime 配置，应用详情页只消费 `DwsClient`，不感知凭据和执行位置。

Alibaba 环境将 Infinity 的 `dws.corp_id` 与 DWS 服务默认 profile 文件同时设置为 `dingd8e1123006514592`。`executor_user_id` 继续校验当前登录用户，值以 wrapper 的 `get-self` 实际响应为准。

## 配置

`dwsd` 增加非敏感配置：

- `DWS_SERVICE_WRAPPER_PATH=/Users/yuanzhan/.qoderwork/bin/dws`

继续使用：

- `DWS_SERVICE_LISTEN_ADDR=127.0.0.1:8002`
- `DWS_SERVICE_PROFILE_FILE=/Users/yuanzhan/Documents/Data/dws-service/secrets/profile`
- `DWS_SERVICE_TOKEN_FILE=/Users/yuanzhan/Documents/Data/dws-service/secrets/http-token`
- `DWS_SERVICE_COMMAND_TIMEOUT=30s`
- `DWS_SERVICE_MAX_BODY_BYTES=1048576`

Bearer 和默认 profile 文件保持 0600。企业登录状态不迁移到 `/Users/yuanzhan/Documents/Data/dws-service`。

## 部署与回滚

唯一发布入口继续是 Jenkins Job `donut-deploy-dws`；Job 从 Docker 发布改为 Snow host LaunchAgent 发布，不另设旁路人工发布入口。

流水线步骤：

1. 检出指定 Git revision，完成代码级静态复核并构建 Darwin arm64 `dwsd`；本次发布不运行测试命令。
2. 将二进制放入带 Git SHA 的不可变 release 目录，记录 SHA-256。
3. 使用 8003 启动候选进程，验证 `/healthz`、`/readyz`、Schema、默认身份和 Alibaba 群基础信息。
4. 候选验证成功后停止当前 8002 Docker 服务，原子切换 active release，并由 LaunchAgent 在 8002 启动。
5. 再次验证 Git revision/二进制摘要、LaunchAgent 状态、HTTP readiness、Alibaba `get-self` 和目标群“构建通知”。
6. 最后验证 Infinity 机器人所在群接口返回群名，而不是只验证 DWS 服务本身。

切换前保留当前 Docker image digest 和 Compose 状态。任一步失败时卸载候选/新 LaunchAgent、恢复该固定旧 image 并重新验证 8002；旧 Docker 仅作为发布回滚点，不作为新服务的运行时 fallback。

## 测试策略

按 TDD 实现，先增加失败测试：

### dingtalk-workspace-cli

- 配置拒绝相对路径、不可执行或可被 group/other 写入的 wrapper。
- HTTP `profile` 能传到 runner；空白、控制字符、CSV 和超长 selector 被拒绝。
- 六个 canonical command 分别生成确定性 argv；未知命令和未知参数被拒绝。
- 字符串、数组、布尔、整数和毫秒时间转换正确。
- 测试 wrapper 验证没有 shell 展开、参数注入或环境变量透传。
- 超时终止整个进程组；stdout/stderr 超限返回稳定错误。
- JSON 成功、结构化认证失败、非 JSON 失败和空输出映射正确。
- readiness 校验默认 corpId，并能在 wrapper 恢复后重新变为 ready。
- HTTP Schema 仍满足 Infinity 的六个 leaf 契约和固定 hash 校验。

### Infinity

- 所有 execute 请求都发送 `profile=settings.corp_id`。
- 启动 `get_current_user` 与群基础信息请求使用同一 profile。
- profile 不取自应用 `org_id`、Client ID、Robot Code 或 openConversationId。
- 现有错误分类、超时预算和响应大小限制不变。

## 验收标准

完成必须同时具备：

1. 目标分支代码级静态复核通过，且没有未审查的通用 argv 执行面。
2. Jenkins 构建成功。
3. Snow 上运行的 LaunchAgent 对应目标 Git revision 和二进制 SHA-256。
4. `127.0.0.1:8002/readyz` 返回 ready，当前用户 corpId 为 Alibaba。
5. HTTP 请求显式指定 Alibaba profile 后，`chat.get_conversation_info` 返回目标群名“构建通知”。
6. Infinity 应用详情页的机器人所在群接口返回群名，并可用该群名创建群助手机器人。

任何一项缺失都不能报告“部署完成”。
