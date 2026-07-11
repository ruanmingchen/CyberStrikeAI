# 工作流 Agent 结构化输出契约 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 为工作流 `agent` 节点提供兼容旧文本输出的 `json_schema` 契约、校验、单次无工具修复、可路由失败和可审计指标。

**Architecture:** `internal/workflow` 集中处理安全 Schema 子集、JSON 提取、校验和诊断；`runAgentNode` 将同一可选契约传给全部 Agent 模式并只处理 final response。结构化对象继续存入既有 `Outputs map[string]any` 与节点输出 JSON，无数据库迁移。

**Tech Stack:** Go 1.25、Eino/ADK、SQLite、原生 JavaScript、JSON i18n。

## Global Constraints

- 缺失 `output_mode` 等价于 `text`；旧图保存、试运行和执行行为不变。
- 仅允许 `string`、`number`、`integer`、`boolean`、`enum` 和同质标量数组，且顶层必须是对象。
- 格式失败不得变成 `no_finding` 或 `has_attack_surface=false`；`route` 不写 `Outputs[output_key]`。
- `repair_attempts` 仅允许 `0` 或 `1`；修复调用无工具、无写入，只使用 final response、Schema、错误。
- 节点记录必须含 Schema 快照、状态、诊断、重试、截断 raw response 和 measured/estimated usage 区分。

---

## File Structure

- Create: `internal/workflow/structured_contract.go` — 配置解析、有限 JSON 解码、值校验、诊断和契约提示。
- Create: `internal/workflow/structured_contract_test.go` — 解析、校验、失败策略和兼容性回归。
- Modify: `internal/workflow/nodes.go`, `structured_outputs.go`, `validation.go`, `dry_run.go`, `metrics.go`, `state.go`, `expression.go` — 运行、审计、校验和模拟。
- Modify: `internal/multiagent/runner.go`, `eino_single_runner.go`, `eino_orchestration.go` — 可选 Schema、无工具修复与 usage。
- Modify: `web/static/js/workflows.js`, `web/static/i18n/zh-CN.json`, `web/static/i18n/en-US.json` — 配置、引用保护和展示。

### Task 1: 契约解析、值校验与图静态校验

**Files:** Create `internal/workflow/structured_contract.go`, `internal/workflow/structured_contract_test.go`; modify `internal/workflow/validation.go`.

**Interfaces:** `parseStructuredOutputContract(map[string]any) (StructuredOutputContract, error)` and `ProcessStructuredResponse(string, StructuredOutputSchema) (map[string]any, StructuredOutputDiagnostic, error)` consume Agent `output_mode`, `output_schema`, and `structured_output`.

- [ ] **Step 1: Write the failing parser test**

```go
func TestProcessStructuredResponseAcceptsSingleJSONFence(t *testing.T) {
 schema := StructuredOutputSchema{ContractVersion: 1, Type: "object", Fields: []StructuredOutputField{{Name: "decision", Type: "enum", Required: true, Enum: []string{"proceed"}}}}
 got, _, err := ProcessStructuredResponse("```json\n{\"decision\":\"proceed\"}\n```", schema)
 require.NoError(t, err); require.Equal(t, "proceed", got["decision"])
}
```

- [ ] **Step 2: Verify RED** — run `go test ./internal/workflow -run TestProcessStructuredResponseAcceptsSingleJSONFence -count=1`; expect undefined API.

- [ ] **Step 3: Implement the smallest contract API**

```go
type StructuredOutputContract struct { Mode string; Schema StructuredOutputSchema; Config StructuredOutputConfig }
func ProcessStructuredResponse(raw string, schema StructuredOutputSchema) (map[string]any, StructuredOutputDiagnostic, error) {
 return decodeAndValidateStructuredObject(exactJSONDocumentOrSingleFence(raw), schema)
}
```

Reject invalid/reserved/duplicate fields, unsupported types, invalid enum/items/length, unknown fields, duplicate JSON keys, top-level arrays and over-limit input/depth.

- [ ] **Step 4: Add graph-validation RED/GREEN cases** for default text compatibility, valid schemas, invalid retry/policy, duplicate Agent root keys, and stale `outputs.<key>.<field>` references. Run `go test ./internal/workflow -run 'Test(ProcessStructured|Validate)' -count=1`; expect PASS.
- [ ] **Step 5: Commit** — `git add internal/workflow/structured_contract.go internal/workflow/structured_contract_test.go internal/workflow/validation.go && git commit -m "feat(workflow): validate structured agent output contracts"`.

### Task 2: Unified final-response result and safe failure routing

**Files:** Modify `internal/workflow/nodes.go`, `structured_outputs.go`, `state.go`; test `structured_contract_test.go` and `expression_join_test.go`.

**Interfaces:** Task 1 produces the contract API. This task produces Agent fields `structured_status`, `structured_value`, `structured_error`, `raw_output`, `output_schema`, `structured_retry_count`.

- [ ] **Step 1: Write the failing route test**

```go
func TestApplyStructuredAgentResultRoutesFormatFailureWithoutOutputValue(t *testing.T) {
 state := newWorkflowLocalState(nil, "run")
 out, completed, status, _ := applyStructuredAgentResult(agentNode("route"), state, "not json", nil)
 require.True(t, completed); require.Equal(t, "completed", status)
 require.Equal(t, "error", out["structured_status"]); require.NotContains(t, state.Outputs, "surface_result")
}
```

- [ ] **Step 2: Verify RED** — run `go test ./internal/workflow -run TestApplyStructuredAgentResult -count=1`; expect undefined result path.
- [ ] **Step 3: Implement shared final-response handling** — append contract instruction in `buildAgentNodeMessage`; for `eino_single`, `deep`, `plan_execute`, and `supervisor`, process only final `result.Response`, write a map only when valid, and implement `route`, `fail`, `text_fallback` without inventing a negative conclusion.
- [ ] **Step 4: Add nested-output and persistence tests** for boolean/enum comparisons, array bindings, checkpoint serialization and Schema snapshot. Run `go test ./internal/workflow -run 'Test(ApplyStructuredAgentResult|Expression|Checkpoint)' -count=1`; expect PASS.
- [ ] **Step 5: Commit** — `git add internal/workflow/nodes.go internal/workflow/structured_outputs.go internal/workflow/state.go internal/workflow/*_test.go && git commit -m "feat(workflow): route structured agent output failures"`.

### Task 3: Optional native schema, repair boundary, and metrics

**Files:** Modify `internal/multiagent/runner.go`, `eino_single_runner.go`, `eino_orchestration.go`, `internal/workflow/nodes.go`, `metrics.go`; test focused multiagent/workflow tests.

**Interfaces:** Produce `WorkflowAgentRunOptions{ResponseSchema map[string]any}` and no-tool `RepairStructuredWorkflowResponse`; consume Task 2 diagnostics and existing usage data.

- [ ] **Step 1: Write the failing repair-boundary test**

```go
func TestStructuredRepairRequestHasNoToolsAndOneAttempt(t *testing.T) {
 req := buildStructuredRepairRequest("bad", schema, []string{"decision is required"})
 require.Empty(t, req.Tools); require.Contains(t, req.Messages[0].Content, "不补充事实")
}
```

- [ ] **Step 2: Verify RED** — run `go test ./internal/multiagent ./internal/workflow -run TestStructuredRepairRequest -count=1`; expect undefined builder.
- [ ] **Step 3: Thread options and repair** — pass one options object through all four modes, prefer provider response-schema support when exposed, otherwise use the contract prompt; retry exactly once with a direct no-tool request and revalidate.
- [ ] **Step 4: Record metrics** — store first/repair prompt and completion tokens, cost plus estimated flag, tool calls, duration, retry count, parser status and error category in existing metrics JSON without inventing usage.
- [ ] **Step 5: Verify GREEN** — run `go test ./internal/multiagent ./internal/workflow -run 'Test(Structured|Eino)' -count=1`; expect PASS.
- [ ] **Step 6: Commit** — `git add internal/multiagent internal/workflow && git commit -m "feat(workflow): add safe structured output repair metrics"`.

### Task 4: Deterministic dry run and visual editor

**Files:** Modify `internal/workflow/dry_run.go`, `web/static/js/workflows.js`, both i18n JSONs; test `structured_contract_test.go`.

**Interfaces:** Consume contract configuration and diagnostics; produce valid simulated objects, serialized editor configuration, blocked stale references and visible diagnostics.

- [ ] **Step 1: Write the failing dry-run test**

```go
func TestDryRunStructuredAgentPublishesSchemaValidObject(t *testing.T) {
 result, err := DryRunGraphJSON(context.Background(), structuredAgentGraph, nil)
 require.NoError(t, err)
 require.Equal(t, true, result.NodeOutputs["agent-1"]["structured_value"].(map[string]any)["has_attack_surface"])
}
```

- [ ] **Step 2: Verify RED** — run `go test ./internal/workflow -run TestDryRunStructuredAgentPublishesSchemaValidObject -count=1`; expect string output failure.
- [ ] **Step 3: Implement UI and simulation** — generate typed examples; add mode selector, isolated field table/type controls, immediate errors, save/load, dependency scan and raw/structured/status/error trial view, retaining text layout and manual input.
- [ ] **Step 4: Verify GREEN** — run `go test ./internal/workflow -run 'Test(DryRun|Validate|Expression)' -count=1` and `node --check web/static/js/workflows.js`; expect PASS.
- [ ] **Step 5: Commit** — `git add internal/workflow/dry_run.go internal/workflow/*_test.go web/static/js/workflows.js web/static/i18n/zh-CN.json web/static/i18n/en-US.json && git commit -m "feat(workflow): configure structured agent output contracts"`.

### Task 5: Cross-mode integration and release gate

**Files:** Modify workflow/multiagent tests from Tasks 1-4 and `docs/zh-CN/workflow-graph.md`, `docs/en-US/workflow-graph.md` if the option is documented.

- [ ] **Step 1: Add four-mode fixtures** covering valid output, `no_finding`, `insufficient_evidence`, malformed route/fail/fallback and distinct concurrent output keys; assert formatting errors never produce negative findings.
- [ ] **Step 2: Add metrics serialization coverage**

```go
func TestStructuredMetricsKeepMeasuredAndEstimatedUsageSeparate(t *testing.T) {
 metrics := structuredMetricsFromUsage(actualUsage, repairUsage)
 require.False(t, metrics["usage_estimated"].(bool)); require.Equal(t, 1, metrics["structured_retry_count"])
}
```

- [ ] **Step 3: Run final verification** — `go test ./internal/workflow ./internal/multiagent -count=1`; `go test ./...`; `node --check web/static/js/workflows.js`; `git diff --check`. All must exit 0; record any baseline failure verbatim.
- [ ] **Step 4: Commit** — `git add docs internal web && git commit -m "test(workflow): cover structured output compatibility"`.

## Plan Self-Review

- Tasks 1-2 cover safe parsing, schema/graph validation, output state, route/fail/fallback, nested reads and backward compatibility.
- Task 3 covers all Agent modes through one option path, native schema fallback, no-tool repair and quality/cost telemetry.
- Tasks 4-5 cover editor, i18n, dry run, references, persistence, concurrent outputs, regression and final checks.
- Contract types are defined before consumers and no implementation placeholder remains.
