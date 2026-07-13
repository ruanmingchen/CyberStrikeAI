# 本地图编排策略包 MVP 设计

## 决策摘要

本期只交付本地图编排策略管理，不接入公共市场、远程仓库、发布上传、账号、评分或订阅能力。目标是先建立稳定的工作流包格式和安全导入闭环；未来市场仅复用该包格式和本地安装器。

## 目标与非目标

目标：用户可将单个工作流导出为可离线传输、可审查的包，并在另一实例中完成预检后显式导入。

本期非目标：

- 批量导出、批量导入、按标签或角色筛选。
- 角色、Skill、工具元数据的实际导出或安装。
- 远程仓库配置、策略市场、下载、上传和发布者身份。
- 自动合并、三方 diff、跨实例 SemVer 升级、降级和回滚。
- 工作流运行记录、会话、项目数据、MCP 密钥或任何可执行载荷。

## 当前基础

- 工作流保存在 SQLite 的 `workflow_definitions`，包含 `id`、`name`、`description`、整型 `version`、`graph_json`、`enabled`。
- 保存前已有严格的 `ValidateGraphJSON` 校验；MVP 导入必须复用它。
- 当前工作流 CRUD、`/validate`、`/dry-run` 和运行 API 不改变。
- 角色和 Skill 分别存放于 `roles/`、`skills/`，本期不写入这两个目录。

## 用户流程

### 导出

1. 用户在图编排详情页选择“导出”。
2. 系统读取单个工作流定义，生成 `.csapkg.zip`。
3. 用户下载包并可解压审查 JSON 与 Manifest。

### 导入

1. 用户在图编排列表页选择“导入本地包”。
2. 系统上传并解析 Zip，但不写入数据库。
3. 系统检查包结构、文件哈希、工作流 JSON，并调用 `ValidateGraphJSON`。
4. 用户查看工作流名称、ID、节点/边数量、`graph_json` hash 和冲突结果。
5. 用户确认“创建”或在冲突时选择“保留本地 / 覆盖 / 另存为新 ID”。
6. 系统写入工作流、失效编译缓存、写入审计日志并返回结果。

导入始终为两步：预检不会写入；仅确认后的应用步骤会改变本地工作流。缺少本期未处理的工具依赖时可显示提示，但不得阻止仅保存定义的导入。

## 包格式

文件扩展名为 `.csapkg.zip`，解压后保持人类可读：

```text
web-src-hunting-1.0.0.csapkg.zip
├─ manifest.json
├─ checksums.sha256
└─ workflows/
   └─ web-src-hunting.json
```

`manifest.json` 示例：

```json
{
  "package_format": "cyberstrikeai.workflow-package",
  "format_version": "1.0",
  "package_id": "pkg_01JWEBHUNT",
  "created_at": "2026-07-13T10:00:00Z",
  "items": [
    {
      "type": "workflow",
      "path": "workflows/web-src-hunting.json",
      "source_id": "web-src-hunting",
      "source_revision": 18,
      "content_hash": "sha256:...",
      "graph_hash": "sha256:..."
    }
  ]
}
```

`workflows/*.json` 保留现有工作流字段。现有整型 `version` 继续作为本地修订号；MVP 不引入 SemVer，也不将版本解释为跨实例升级语义。

## 冲突规则

| 目标状态 | 默认行为 | 可选动作 |
|---|---|---|
| 本地不存在同 ID | 创建 | 无 |
| 本地存在同 ID | 保留本地并报告冲突 | 覆盖、另存为新 ID、取消 |
| 包内容 hash 与本地一致 | 跳过，视为幂等成功 | 无 |

覆盖是破坏性操作，必须二次确认。另存为新 ID 时仅修改导入副本 ID，不修改任何角色绑定。

## 后端边界

新增 `internal/workflow/package` 包：

- `manifest.go`：包格式、JSON 解析和版本兼容。
- `exporter.go`：从 `workflow_definitions` 生成 Zip。
- `inspector.go`：安全解压、哈希校验、结构校验与 `ValidateGraphJSON` 复用。
- `importer.go`：冲突策略、数据库写入和编译缓存失效。

建议新增 API：

| API | 语义 | 写入 / 确认 |
|---|---|---|
| `POST /api/workflow-packages/exports` | 生成并下载单工作流包 | 无写入；无需确认 |
| `POST /api/workflow-packages/inspections` | 上传并预检包 | 无写入；无需确认 |
| `POST /api/workflow-package-imports` | 创建并应用导入计划 | 有写入；覆盖时需确认 |

现有 API 不变。权限建议沿用 `workflow:read` 用于导出，`workflow:write` 用于导入；若后续需要细粒度授权，再拆出 `workflow:export`、`workflow:import`。

## 安全与审计

- 仅允许 Manifest 与声明的 JSON 文本；拒绝 Zip 路径穿越、重复条目、软链接、超大解压和未知文件。
- 校验每个包项 SHA-256。
- 不导出或导入密钥、Token、MCP 连接配置、运行记录和可执行文件。
- 预检与实际导入分别写审计日志；应用记录包 hash、工作流 ID、策略和结果。

## 前端范围

- 图编排列表页：增加“导入本地包”。
- 图编排详情页：增加“导出”。
- 导入 Modal：上传、预检结果、冲突策略和确认应用。
- `workflows.js` 负责调用新 API；不增加策略市场、远程仓库或发布 UI。

## 验收与测试

- 导出 `web-src-hunting` 后可解压并审查 Manifest 与完整工作流 JSON。
- 导入包在确认前不改变数据库。
- 合法图可创建；非法 DAG 或节点参数由 `ValidateGraphJSON` 拒绝。
- 同 ID 默认不覆盖；覆盖须确认；另存生成新 ID。
- 包 hash 不匹配、路径穿越、未知文件、超限文件均被拒绝。
- 成功导入后工作流可由现有 GET API 读取，且编译缓存已失效。

## 后续扩展边界

v1 可增加批量导入导出、Role/Skill 可选项、工具依赖展示与导入历史。v2 可基于同一 `.csapkg.zip` 增加语义标识、SemVer、升级 diff、三方合并与回滚。公共策略市场、远程仓库和发布上传属于 v3 之后的独立子项目，不进入本期实现。
