# 工作流 Agent 结构化输出契约设计

日期：2026-07-11  
状态：待评审  
范围：CyberStrikeAI 图编排中的 `agent` 节点

## 1. 背景与目标

现有工作流 Agent 节点只有 `output_key`。运行时会把 `result.Response` 作为文本直接保存到 `state.Outputs[output_key]`。这适合展示报告，但条件分支、工具参数和后续节点只能依赖文本匹配或再次让模型理解文本，容易受措辞变化、格式错误和上下文截断影响。

本功能将 Agent 节点的“输出变量名”扩展为一个可配置的**输出契约**：节点仍可输出自然语言；也可要求模型从自然语言输入中提取并返回符合用户定义 Schema 的 JSON。通过后端校验后，将 JSON 对象作为一个命名输出写回工作流状态。

目标：

- 让工作流作者在页面上定义 Agent 结果字段，而非为每类业务输出编写后端代码。
- 保持既有 `text` 节点、工作流定义和运行行为完全兼容。
- 让条件、工具和后续 Agent 能稳定读取布尔、枚举、数组和对象字段。
- 对结构化失败提供可观察、有限重试和可路由的结果，禁止把格式失败误判成业务否定结论。
- 对 `eino_single`、`deep`、`plan_execute`、`supervisor` 的工作流 Agent 节点保持同一契约。

非目标（第一期不做）：

- 不新增独立“Transform”画布节点；Agent 节点属性是主路径。
- 不为任意 MCP 工具或外部 HTTP 原始输出做通用 AI 转换。
- 不实现完整 JSON Schema 2020-12；仅实现用户界面能表达、运行时能可靠验证的安全子集。
- 不把结构化数据自动扁平化为 `outputs` 根键。

## 2. 用户体验与业务示例

用户配置“Web 暴露面”Agent：

```text
输入字段绑定：outputs / recon_result
输出变量名：surface_result
输出模式：结构化 JSON
失败策略：路由至结构化失败分支
最大修复次数：1
```

在“输出字段”表格中定义：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `decision` | 枚举 | 是 | `proceed`、`no_finding`、`insufficient_evidence`、`needs_review` 之一 |
| `has_attack_surface` | 布尔 | 是 | 是否有足够证据继续验证攻击面 |
| `summary` | 字符串 | 是 | 给人工阅读的简洁结论 |
| `web_endpoints` | 字符串数组 | 否 | 已发现的相关路径/API |
| `evidence` | 字符串数组 | 否 | 支撑结论的证据摘要 |

成功时写入：

```go
state.Outputs["surface_result"] = map[string]any{
    "decision": "proceed",
    "has_attack_surface": true,
    "summary": "发现公开 API、管理入口与上传接口。",
    "web_endpoints": []any{"/api/v1", "/admin", "/upload"},
}
```

下游使用读取路径，而不是再次赋值：

```text
{{outputs.surface_result.decision}} == "proceed"
{{outputs.surface_result.has_attack_surface}} == true
{{outputs.surface_result.web_endpoints}}
```

`false` 只表示“已完成足够范围的检查且未发现攻击面”；证据不足必须使用 `decision=insufficient_evidence` 或 `needs_review`，不能借用 `false`。

## 3. 方案选择

### 3.1 备选方案

1. 仅提示词要求模型返回 JSON：改动最少，但没有类型校验、错误状态、重试和可视化字段发现能力，不采用。
2. 新增独立转换节点：可适配工具/外部输出，但同一 Agent 需要先生成文本、再被第二个模型解析，增加成本、延迟和二次理解偏差；第一期不作为主路径。
3. 在 Agent 节点增加输出契约：同一次 Agent 调用同时完成语义分析和字段提取，后端统一解析与校验；采用。

### 3.2 后续扩展

第二期可新增独立“转换/校验”节点，复用本期 Schema、解析器、失败策略和运行记录，用于 MCP 工具、HTTP API 或历史文本输出。不要在第一期复制一套校验代码。

## 4. 图配置协议

旧 Agent 配置保持有效：

```json
{
  "agent_mode": "eino_single",
  "input_binding": {"from": "previous", "field": "output"},
  "instruction": "...",
  "output_key": "agent_result"
}
```

新字段全部可选；缺失时等价于 `output_mode=text`：

```json
{
  "agent_mode": "eino_single",
  "input_binding": {"from": "outputs", "field": "recon_result"},
  "instruction": "在授权范围内分析 Web 暴露面。",
  "output_key": "surface_result",
  "output_mode": "json_schema",
  "output_schema": {
    "contract_version": 1,
    "type": "object",
    "fields": [
      {
        "name": "decision",
        "type": "enum",
        "required": true,
        "enum": ["proceed", "no_finding", "insufficient_evidence", "needs_review"],
        "description": "后续流程应如何处理"
      },
      {
        "name": "has_attack_surface",
        "type": "boolean",
        "required": true,
        "description": "是否有足够证据继续验证攻击面"
      },
      {"name": "summary", "type": "string", "required": true, "max_length": 2000},
      {"name": "web_endpoints", "type": "array", "items": {"type": "string"}, "required": false}
    ]
  },
  "structured_output": {
    "repair_attempts": 1,
    "failure_policy": "route"
  }
}
```

字段名规则：

- 字段名使用 `[A-Za-z_][A-Za-z0-9_]*`，不得包含 `.`、`-` 或空格。
- 不允许重复；不允许 `status`、`error`、`raw_output`、`schema_version`、`structured_status` 等保留名。
- `output_key` 仍由现有规则决定；结构化模式下它是对象根键，不能与其他节点的根输出键冲突。
- 支持类型：`string`、`number`、`integer`、`boolean`、`enum`、`array<string>`、`array<number>`、`array<integer>`、`array<boolean>`。嵌套 `object`、联合类型、任意 JSONPath 映射列入第二期。
- 每个字段有 `description`，该说明是模型字段语义的唯一配置入口；不要把不可信上游文本拼入 Schema 元数据。

## 5. 前端设计

修改 `web/static/js/workflows.js` 中 Agent 节点属性面板和 `readTypedConfig`：

1. 在“输出变量名”后增加“输出模式”下拉：`自然语言（text）`、`结构化 JSON（json_schema）`。
2. 选 `text` 时保持当前界面；选 `json_schema` 时显示字段表、重试次数和失败策略。
3. 字段表支持新增、删除、排序；每行包括名称、类型、必填、说明，以及按类型出现的枚举值/数组元素类型/最大长度。
4. “自定义字段”区域不可再被误认为结构化字段；保留其现有含义，结构化字段使用单独区域“输出字段契约”。
5. 字段编辑即时校验名称、重复、枚举空值、数组元素类型；保存前显示所有错误。
6. 条件/工具/Agent 的字段绑定下拉应优先展示已知上游结构化路径，例如 `outputs.surface_result.decision`。手工填写仍保留，以兼容动态工作流。
7. 当编辑 Schema 删除或修改已被下游引用的字段，保存前扫描 `input_binding`、`source_binding`、工具参数绑定和条件表达式并阻止保存，显示引用节点。
8. 试运行面板要展示 `raw_output`、`structured_value`、`structured_status` 和校验错误；不得将模拟字符串当作有效结构化结果。

补充 i18n：`web/static/i18n/zh-CN.json`、`web/static/i18n/en-US.json`。

## 6. 后端运行时设计

### 6.1 新的内部类型

在 `internal/workflow/` 新增 `structured_contract.go`，定义：

```go
type StructuredOutputSchema struct {
    ContractVersion int                     `json:"contract_version"`
    Type            string                  `json:"type"`
    Fields          []StructuredOutputField `json:"fields"`
}

type StructuredOutputField struct {
    Name        string   `json:"name"`
    Type        string   `json:"type"`
    Required    bool     `json:"required"`
    Description string   `json:"description"`
    Enum        []string `json:"enum,omitempty"`
    Items       *StructuredOutputItems `json:"items,omitempty"`
    MaxLength   int      `json:"max_length,omitempty"`
}

type StructuredOutputConfig struct {
    RepairAttempts int    `json:"repair_attempts"`
    FailurePolicy  string `json:"failure_policy"` // route | fail | text_fallback
}
```

上面的配置 Schema 与运行时 JSON 值必须分离：Schema 是节点定义；值是 `map[string]any`，只能包含 JSON 可序列化值。

### 6.2 调用与结果处理顺序

`runAgentNode` 继续执行当前选定的 Agent 模式，拿到**最终** `result.Response`。仅 final response 进入结构化处理；规划内容、流式 delta、MCP 工具输出、内部推理事件不能参与解析。

```text
构建节点消息（含字段说明）
  → 调用当前 Agent
  → 得到 final response
  → text：按现有逻辑保存字符串
  → json_schema：提取 JSON、解析、Schema 校验
       ├─ 成功：保存 map[string]any
       └─ 失败：可选一次无工具格式修复，再校验
            ├─ 成功：保存 map[string]any，记录 retry=1
            └─ 失败：按 failure_policy 生成结构化失败结果
```

`buildAgentNodeMessage` 要追加明确契约提示：只返回 JSON、不可添加 Markdown 围栏、字段说明、未知事实的处理规则。但提示词只是回退保障；优先将转换后的 JSON Schema 传入模型供应商的原生 structured-output/response-format 能力。是否支持原生能力由模型适配层决定；不支持时才使用提示词 + 后端校验。

当前 `RunEinoSingleChatModelAgent` 和 `RunDeepAgent` 的调用参数没有输出 Schema。实现时应新增一个可选 `WorkflowAgentRunOptions`（或等价的可选参数对象）承载 `ResponseSchema`，再从 `internal/workflow/nodes.go` 向下传递。不要为不同模式在工作流层复制四套解析逻辑。

### 6.3 JSON 提取、解析和校验

解析器职责：

1. 尝试直接解析完整响应。
2. 仅在直接解析失败时，谨慎剥离单个完整 Markdown `json` 代码块后重试。
3. 不从任意自然语言中“猜一个 JSON 子串”；这会把模型解释文字或攻击载荷误作有效数据。
4. 解码为 `map[string]any`，拒绝顶层数组、重复键、超出大小/深度限制的输入。
5. 按上节字段 Schema 验证必填、未知字段策略、类型、枚举、数组元素、最大长度。

第一期未知字段默认拒绝，确保输出契约稳定。后续可按节点配置开放 `additional_fields`。

### 6.4 重试与安全边界

`repair_attempts` 只允许 `0` 或 `1`，默认 `1`。修复调用必须使用**无工具、无写入能力**的直接模型调用，输入仅包含：原始 final response、Schema、结构化校验错误。严禁重跑整个 Agent，因为这可能重复扫描、写事实、记录漏洞或调用其他有副作用的 MCP 工具。

修复提示必须要求：不补充事实；无法确定时仅按 Schema 使用 `needs_review`/`insufficient_evidence`（若该枚举存在）。记录原始输出但在 UI/日志中按现有敏感信息策略脱敏、截断。

### 6.5 成功与失败状态

成功时：

```go
state.Outputs[outputKey] = structuredValue
agentOutput["output"] = structuredValue
agentOutput["structured_status"] = "valid"
agentOutput["structured_retry_count"] = retryCount
```

结构化失败但 `failure_policy=route` 时，节点本身仍以 `completed` 继续图执行，**不写** `state.Outputs[outputKey]`，并写入节点输出：

```json
{
  "structured_status": "error",
  "structured_error": "summary is required",
  "raw_output": "...",
  "output": ""
}
```

后续条件可路由：

```text
{{previous.structured_status}} == "error"
```

`failure_policy=fail` 才沿用当前节点失败并停止的语义。`text_fallback` 仅用于纯人工阅读路径：写入原始文本，同时标记 `structured_status=fallback_text`；禁止该分支再自动执行安全工具或以其驱动风险否定结论。

### 6.6 输出与展示

`summary` 不是框架保留字段，但安全研判模板应将其设为必填。聊天最终展示规则：

- 若 Schema 存在名为 `summary` 的非空字符串，向用户展示它；
- 否则展示格式化 JSON；
- 运行详情始终保存并可查看 raw response、规范化对象、Schema 快照、解析路径、校验错误和重试次数。

这避免聊天区只呈现机器 JSON，同时保持审计可复现性。

## 7. 图校验、状态、并发与持久化

- 在 `internal/workflow/validation.go` 增加 Agent `output_mode`、Schema 和重试/失败策略静态校验。
- `WorkflowLocalState.Outputs` 已是 `map[string]any`，`valueFromPath` 已支持嵌套 map 路径；无需为结构化对象改变状态模型。
- `NodeOutputs`、工作流 checkpoint、`workflow_runs.output_json` 与 `workflow_node_runs.output_json` 必须能完整序列化结构化对象和诊断信息。
- 并行分支应保持每个节点写入自己的 `output_key`；禁止两个节点共享同一根键。多上游 `all_merge` 的 `previous.output` 可能为数组，不能将其自动当作结构化输出对象。
- 工作流定义目前以 `graph_json` 不透明存储；新增配置字段不需数据库迁移，也不应更改旧图的 `schema_version: 1`。`output_schema.contract_version` 独立演进。
- 图保存要以当前定义验证；运行记录必须附带 Schema 快照，确保日后 Schema 修改后仍能解释旧运行结果。

## 8. 代码改动清单

| 位置 | 改动 |
| --- | --- |
| `web/static/js/workflows.js` | Agent 输出模式、Schema 字段表、读取/保存配置、依赖字段扫描、试运行显示。 |
| `web/static/i18n/zh-CN.json`、`en-US.json` | 新字段、校验错误、状态与帮助文案。 |
| `internal/workflow/structured_contract.go`（新增） | 配置解析、Schema 校验、JSON 提取、值校验、诊断对象。 |
| `internal/workflow/nodes.go` | 构造契约消息、传 Schema 给 Agent、对 final response 执行结构化处理、写入对象或可路由错误。 |
| `internal/workflow/validation.go` | 静态校验 Agent 结构化配置及输出键冲突。 |
| `internal/workflow/structured_outputs.go` | 给 `AgentOutput` 添加结构化状态、规范化值、原始响应摘要、重试次数。 |
| `internal/workflow/dry_run.go` | 对结构化 Agent 生成 Schema 合法的模拟对象，并验证条件/字段绑定。 |
| `internal/workflow/state.go`、`expression.go` | 验证嵌套对象、布尔/数组比较与 JSONPath 能正确读取；仅在缺口存在时最小修改。 |
| `internal/multiagent/` 的运行入口与模型适配层 | 增加可选 ResponseSchema，优先走供应商原生结构化输出；提供无工具 JSON 修复调用。 |
| 工作流运行记录/API DTO | 增加结构化诊断字段并确保敏感字段脱敏。 |

## 9. 测试与验收

### 单元测试

- 旧 Agent 配置默认 `text`，输出仍是字符串。
- Schema 字段名、类型、必填、枚举、数组元素类型、最大长度、保留名、重复名的静态校验。
- 直接 JSON、单个 JSON 代码块、非法 JSON、顶层数组、超大输入、未知字段、嵌套路径读取。
- `{{outputs.surface_result.has_attack_surface}} == true`、枚举判断、数组作为工具参数绑定。
- 格式修复成功、修复后仍失败、`route`/`fail`/`text_fallback` 三种失败策略。
- 修复调用断言无 MCP 工具、无项目事实/漏洞写入；最大仅一次。
- checkpoint 和数据库运行记录的序列化/反序列化。

### 集成测试

- 四种 Agent 模式各有一条结构化 Agent 工作流，均只解析 final response。
- 结构化成功进入“有攻击面”分支；`decision=no_finding` 进入无攻击面分支；结构化错误进入人工审核分支。
- 并发分支使用不同 `output_key`，汇聚后字段不串值。
- 修改或删除已被下游引用的字段会在保存时被拒绝。
- 试运行以结构化模拟对象通过字段条件，不调用模型、工具、审批。

### 验收指标

- 旧工作流保存、试运行、执行无行为回归。
- 合法结构化响应首次解析成功率、修复成功率、最终结构化失败率和重试开销均写入运行指标。
- 结构化失败绝不自动被解释为 `no_finding` 或 `has_attack_surface=false`。
- 失败路由可在运行详情中明确显示原因；用户可查看摘要而无需阅读完整 JSON。

## 10. A/B 效果与成本验证（上线硬性门槛）

本功能不能以“JSON 能解析”作为有效性证明。必须证明它相对当前纯文本工作流，能以可接受成本提升自动路由与下游执行质量。A/B 验证是发布前硬性门槛，并在灰度期间持续观测。

### 10.1 分组定义

| 组别 | 节点配置 | 路由方式 |
| --- | --- | --- |
| A（基线） | 当前 `output_mode=text`，保持原节点指令和 `output_key` | 现有文本表达式/关键词/人工复核；不得新增 JSON 提示或解析器。 |
| B（实验） | `output_mode=json_schema`，使用同一业务指令、同一模型、同一上游输入、相同工具权限 | 读取 Schema 字段；启用后端校验和最多一次无工具格式修复。 |

公平性要求：模型、模型版本、温度、最大输出、系统提示、上游输入、角色工具、授权范围和网络环境必须保持一致。B 组只允许新增 Schema 契约、原生 structured-output 参数（若供应商支持）和格式修复调用。任何提示词业务优化必须同时应用到 A/B 两组，或另立实验。

### 10.2 离线金标集

上线前建立可重复执行的安全工作流评估集。每条样本包括：脱敏上游侦察结果、期望路由、期望字段、证据依据，以及至少两名安全评审者的独立标注；有分歧时由第三位评审裁决。

评估集至少覆盖：

- 有明确 Web 攻击面、无攻击面、证据不足、需要人工复核四种决策。
- API、上传、认证/授权、管理入口、URL Fetch、纯 IP 非 Web 目标、WAF/CDN、空输入和冲突证据。
- 正常 JSON、模型包裹 Markdown、字段缺失、枚举错误、布尔值写成“是/否”、超长响应等结构化失败样本。
- 高价值/高风险路由的边界案例；这类样本不得只以关键词命中作为金标。

首轮建议不少于 200 个去重样本，并按决策类别分层。为量化模型随机性，每个样本在固定配置下至少运行 3 次；报告均值、分位数和最差值，而非只报告一次成功率。评估集、Schema 版本、工作流版本和模型版本必须随报告存档。

### 10.3 必测指标

#### 业务效果

| 指标 | 定义 | 目标 |
| --- | --- | --- |
| 路由准确率 | 实际分支与金标分支一致的比例 | B 高于 A，且差异可复现。 |
| `proceed` 精确率/召回率/F1 | 以“继续漏洞评估”为正类计算 | 优先控制误放行，同时不显著降低召回。 |
| 关键漏路由率 | 金标应进入安全验证却被路由到 `no_finding` 的比例 | B 必须低于 A；为安全关键门槛。 |
| 关键误路由率 | 金标不应自动验证却被 `proceed` 的比例 | B 必须低于或不劣于 A。 |
| 字段语义正确率 | 每个字段与金标一致或被评审判定可接受的比例 | 分字段报告，不能只报整对象解析率。 |
| 证据可复核率 | `summary/evidence` 能被评审定位至上游事实或工具输出的比例 | B 不得因压缩为字段而下降。 |
| 下游参数有效率 | 工具节点接收到类型正确、非空、在授权范围内的参数比例 | B 应高于 A。 |
| 人工纠正率 | 人工审核推翻自动路由或字段的比例 | B 应低于 A。 |

`has_attack_surface=false` 只能在金标为“已充分检查且未发现”时算正确；`insufficient_evidence` 与 `needs_review` 不能被合并计为 false。

#### 结构化可靠性

| 指标 | 定义 |
| --- | --- |
| 首次 Schema 有效率 | 首次 Agent final response 直接通过 JSON 解析和 Schema 校验的比例。 |
| 修复成功率 | 首次失败、一次无工具修复后通过的比例。 |
| 最终结构化失败率 | 修复后仍无法通过的比例；按模型、工作流、字段和错误类型分组。 |
| 错误静默率 | 结构化失败却被当作正常业务值继续自动路由的比例；目标必须为 0。 |
| 字段缺失/类型/枚举错误分布 | 每种校验错误的计数与占比，用于反馈 Schema 或模型适配层。 |

#### 性能与成本

必须比较“单个节点”与“完整工作流”两种口径，因为 B 组可能多一次修复调用，但也可能减少下游重复 Agent 调用、无效工具调用和人工处理。

| 指标 | 计算方式 |
| --- | --- |
| 节点输入 token | 首次 Agent 调用输入 token；B 的 Schema/字段说明增量单独记录。 |
| 节点输出 token | 首次 final response 输出 token。 |
| 修复 token | 修复调用的输入 token + 输出 token；A 组恒为 0。 |
| 节点总 token | 首次输入 + 首次输出 + 修复 token。 |
| 工作流总 token | 所有 Agent/修复调用 token 之和，包含因路由不同产生的下游调用。 |
| 每个正确路由的 token | 工作流总 token / 正确路由数；用于避免“准确但成本失控”。 |
| 模型成本 | 按各模型输入/输出单价计算；无供应商 usage 时记录估算值并明确标记。 |
| 端到端时延 | 从工作流开始到终态的 p50/p95；修复调用时单独记录修复耗时。 |
| 工具成本/次数 | MCP 调用数、失败数、被拒绝数；观察 B 是否减少无效后续调用。 |

优先读取模型响应中的实际 usage/cost；若某适配层未提供 usage，使用统一 tokenizer 估算并将字段标为 `estimated=true`，不得和实际 usage 混合比较。运行记录须保存 `prompt_tokens`、`completion_tokens`、`repair_prompt_tokens`、`repair_completion_tokens`、`model_cost`、`tool_call_count`、`duration_ms` 和 `structured_retry_count`。现有工作流 `Metrics` 与节点运行记录是这些指标的落点。

### 10.4 执行步骤

1. 锁定同一工作流版本的 A/B 图定义，仅将 Agent 输出契约相关字段作为差异。
2. 将金标集随机打乱，按样本 ID 成对执行 A、B；同一轮中随机化先后顺序，避免缓存、限流或目标状态影响某一组。
3. 每条样本重复 3 次，采集节点输出、路由、字段、模型 usage、时延、工具调用与错误状态。
4. 自动计算准确率、精确率、召回率、F1、失败率、token/成本和 p50/p95；对成对结果报告差值及置信区间或至少报告逐样本差异分布。
5. 由不知道分组的安全评审者复核抽样的 `summary/evidence` 与关键误路由，避免只以自动规则自证。
6. 仅当 B 同时满足质量和成本门槛，才进入影子运行；不得因为 JSON 首次成功率高而跳过业务路由评估。

### 10.5 建议发布门槛

以下数值是首期建议的准入线；基线数据收集后可由产品与安全负责人调整，但调整需记录理由：

```text
最终结构化失败率          <= 1%
错误静默率                = 0
B 的关键漏路由率          < A，且没有新增高风险类别回退
B 的路由 F1               >= A + 5 个百分点（或统计/评审证实有实质提升）
B 的人工纠正率            <= A
B 的每正确路由总 token    不高于 A 的 120%
B 的端到端 p95 时延       不高于 A 的 130%
```

若 B 的节点 token 上升但工作流总 token、无效工具调用或人工纠正显著下降，应以“每正确路由的总成本”和人工复核结论作最终决策，不能只看单节点 token。

### 10.6 线上影子与灰度

1. **影子运行**：对已授权、低风险或回放任务，B 在后台生成结构化结果但不实际驱动工具/写入/分支；与 A 的实际结果和后续人工结果对比。
2. **小流量灰度**：先仅用于低风险、已有 HITL 的流程；结构化 `decision=proceed` 仍经审批，避免新路由直接扩大安全测试动作。
3. **逐步放量**：按工作流和角色单独开关，10% → 25% → 50% → 100%；任一阶段触发关键漏路由、错误静默或成本门槛即自动回退 `text`。
4. **持续看板**：按模型、工作流、Schema 版本、Agent 模式和字段维度追踪上述指标；Schema 或模型升级后重新进入影子验证。

## 11. 实施顺序

1. 定义并测试后端 Schema 配置解析、值校验和诊断模型。
2. 在 Agent 节点引入 `text/json_schema` 分支；先以提示词 + 后端校验落地，保证默认兼容。
3. 引入无工具单次修复与 `route/fail/text_fallback` 语义，补足运行记录。
4. 打通模型适配层的原生 structured-output 能力，作为可用时优先路径。
5. 实现前端下拉、字段编辑器、保存校验、字段引用提示和试运行展示。
6. 补齐单元/集成/回归测试，使用真实的安全工作流样例验证。
7. 发布说明：旧节点默认文本；推荐安全研判 Schema 的 `decision`、`summary`、`evidence`、`confidence` 模板。

## 12. 风险与决策记录

- 不把“JSON 模式”宣传为模型事实准确性保证；它只保证可解析数据契约，证据质量仍取决于模型、工具和授权范围。
- 不允许结构化失败静默降级为业务结论。
- 不建议第一期实现任意嵌套对象，以降低 UI、表达式和 Schema 演进复杂度。
- 不建议格式修复调用携带工具权限，避免重复产生安全测试副作用。
- `summary` 与 `evidence` 应被鼓励而非框架强制，以保留通用性；安全模板默认强制它们。
