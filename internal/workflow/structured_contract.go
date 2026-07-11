package workflow

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	structuredOutputModeText       = "text"
	structuredOutputModeJSONSchema = "json_schema"
	structuredStatusValid          = "valid"
	structuredStatusError          = "error"
	structuredStatusFallbackText   = "fallback_text"

	maxStructuredResponseBytes = 128 * 1024
	maxStructuredJSONDepth     = 16
)

var structuredFieldNameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var structuredFenceRE = regexp.MustCompile("(?is)^\\s*```json\\s*\\r?\\n(.*?)\\r?\\n?```\\s*$")
var structuredReservedFields = map[string]struct{}{
	"status": {}, "error": {}, "raw_output": {}, "schema_version": {}, "structured_status": {},
}

// StructuredOutputSchema is the safe, UI-expressible JSON Schema subset stored on an agent node.
type StructuredOutputSchema struct {
	ContractVersion int                     `json:"contract_version"`
	Type            string                  `json:"type"`
	Fields          []StructuredOutputField `json:"fields"`
}

type StructuredOutputField struct {
	Name        string                 `json:"name"`
	Type        string                 `json:"type"`
	Required    bool                   `json:"required"`
	Description string                 `json:"description"`
	Enum        []string               `json:"enum,omitempty"`
	Items       *StructuredOutputItems `json:"items,omitempty"`
	MaxLength   int                    `json:"max_length,omitempty"`
}

type StructuredOutputItems struct {
	Type string `json:"type"`
}

type StructuredOutputConfig struct {
	RepairAttempts int    `json:"repair_attempts"`
	FailurePolicy  string `json:"failure_policy"`
}

type StructuredOutputContract struct {
	Mode   string
	Schema StructuredOutputSchema
	Config StructuredOutputConfig
}

type StructuredOutputDiagnostic struct {
	Status       string   `json:"structured_status"`
	Error        string   `json:"structured_error,omitempty"`
	ParsePath    string   `json:"structured_parse_path,omitempty"`
	Schema       any      `json:"output_schema,omitempty"`
	ErrorDetails []string `json:"structured_error_details,omitempty"`
}

func parseStructuredOutputContract(cfg map[string]any) (StructuredOutputContract, error) {
	contract := StructuredOutputContract{
		Mode:   structuredOutputModeText,
		Config: StructuredOutputConfig{RepairAttempts: 1, FailurePolicy: "route"},
	}
	if cfg == nil {
		return contract, nil
	}
	if mode := strings.ToLower(strings.TrimSpace(cfgString(cfg, "output_mode"))); mode != "" {
		contract.Mode = mode
	}
	if contract.Mode == structuredOutputModeText {
		return contract, nil
	}
	if contract.Mode != structuredOutputModeJSONSchema {
		return StructuredOutputContract{}, fmt.Errorf("output_mode 仅支持 text 或 json_schema")
	}
	schema, err := decodeStructuredSchema(cfg["output_schema"])
	if err != nil {
		return StructuredOutputContract{}, err
	}
	if err := validateStructuredSchema(schema); err != nil {
		return StructuredOutputContract{}, err
	}
	contract.Schema = schema
	if raw, ok := cfg["structured_output"]; ok && raw != nil {
		if err := decodeViaJSON(raw, &contract.Config); err != nil {
			return StructuredOutputContract{}, fmt.Errorf("structured_output 非法: %w", err)
		}
	}
	if contract.Config.RepairAttempts < 0 || contract.Config.RepairAttempts > 1 {
		return StructuredOutputContract{}, fmt.Errorf("repair_attempts 只允许 0 或 1")
	}
	contract.Config.FailurePolicy = strings.ToLower(strings.TrimSpace(contract.Config.FailurePolicy))
	if contract.Config.FailurePolicy == "" {
		contract.Config.FailurePolicy = "route"
	}
	switch contract.Config.FailurePolicy {
	case "route", "fail", "text_fallback":
	default:
		return StructuredOutputContract{}, fmt.Errorf("failure_policy 仅支持 route、fail 或 text_fallback")
	}
	return contract, nil
}

func decodeStructuredSchema(raw any) (StructuredOutputSchema, error) {
	var schema StructuredOutputSchema
	if raw == nil {
		return schema, fmt.Errorf("json_schema 模式必须配置 output_schema")
	}
	if value, ok := raw.(StructuredOutputSchema); ok {
		return value, nil
	}
	if err := decodeViaJSON(raw, &schema); err != nil {
		return schema, fmt.Errorf("output_schema 非法: %w", err)
	}
	return schema, nil
}

func decodeViaJSON(raw any, target any) error {
	encoded, err := json.Marshal(raw)
	if err != nil {
		return err
	}
	return json.Unmarshal(encoded, target)
}

func validateStructuredSchema(schema StructuredOutputSchema) error {
	if schema.ContractVersion != 1 {
		return fmt.Errorf("output_schema.contract_version 必须为 1")
	}
	if strings.ToLower(strings.TrimSpace(schema.Type)) != "object" {
		return fmt.Errorf("output_schema.type 必须为 object")
	}
	if len(schema.Fields) == 0 {
		return fmt.Errorf("output_schema.fields 至少需要一个字段")
	}
	seen := make(map[string]struct{}, len(schema.Fields))
	for _, field := range schema.Fields {
		name := strings.TrimSpace(field.Name)
		if !structuredFieldNameRE.MatchString(name) {
			return fmt.Errorf("输出字段名 %q 非法", field.Name)
		}
		if _, reserved := structuredReservedFields[strings.ToLower(name)]; reserved {
			return fmt.Errorf("输出字段名 %q 为保留名", name)
		}
		if _, exists := seen[name]; exists {
			return fmt.Errorf("输出字段名 %q 重复", name)
		}
		seen[name] = struct{}{}
		if strings.TrimSpace(field.Description) == "" {
			return fmt.Errorf("输出字段 %q 必须填写说明", name)
		}
		switch strings.ToLower(strings.TrimSpace(field.Type)) {
		case "string", "number", "integer", "boolean":
			if len(field.Enum) > 0 || field.Items != nil {
				return fmt.Errorf("输出字段 %q 的类型配置冲突", name)
			}
		case "enum":
			if len(field.Enum) == 0 {
				return fmt.Errorf("枚举字段 %q 至少需要一个值", name)
			}
			values := map[string]struct{}{}
			for _, value := range field.Enum {
				value = strings.TrimSpace(value)
				if value == "" {
					return fmt.Errorf("枚举字段 %q 不能包含空值", name)
				}
				if _, exists := values[value]; exists {
					return fmt.Errorf("枚举字段 %q 存在重复值", name)
				}
				values[value] = struct{}{}
			}
		case "array":
			if field.Items == nil || !isStructuredScalarType(field.Items.Type) {
				return fmt.Errorf("数组字段 %q 必须指定支持的 items.type", name)
			}
			if len(field.Enum) > 0 {
				return fmt.Errorf("数组字段 %q 不支持 enum", name)
			}
		default:
			return fmt.Errorf("输出字段 %q 的类型 %q 不受支持", name, field.Type)
		}
		if field.MaxLength < 0 || (field.MaxLength > 0 && strings.ToLower(strings.TrimSpace(field.Type)) != "string") {
			return fmt.Errorf("输出字段 %q 的 max_length 仅可用于 string", name)
		}
	}
	return nil
}

func isStructuredScalarType(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "string", "number", "integer", "boolean":
		return true
	default:
		return false
	}
}

func structuredOutputInstruction(schema StructuredOutputSchema) string {
	encoded, err := json.Marshal(schema)
	if err != nil {
		return "输出必须是符合已配置字段契约的单个 JSON 对象；不要输出 Markdown 代码围栏或解释文字。"
	}
	return "输出必须是单个 JSON 对象，不得使用 Markdown 代码围栏或解释文字。严格遵循以下输出契约；不要补充未知事实，证据不足时使用契约中可用的 needs_review 或 insufficient_evidence 枚举值：\n" + string(encoded)
}

func structuredDryRunValue(schema StructuredOutputSchema) map[string]any {
	value := make(map[string]any, len(schema.Fields))
	for _, field := range schema.Fields {
		switch strings.ToLower(strings.TrimSpace(field.Type)) {
		case "enum":
			if len(field.Enum) > 0 {
				value[field.Name] = field.Enum[0]
			}
		case "string":
			value[field.Name] = "[dry-run] " + field.Name
		case "number":
			value[field.Name] = float64(1)
		case "integer":
			value[field.Name] = int64(1)
		case "boolean":
			value[field.Name] = true
		case "array":
			value[field.Name] = []any{structuredDryRunPrimitive(field.Items.Type, field.Name)}
		}
	}
	return value
}

func structuredDryRunPrimitive(typeName, name string) any {
	switch strings.ToLower(strings.TrimSpace(typeName)) {
	case "number":
		return float64(1)
	case "integer":
		return int64(1)
	case "boolean":
		return true
	default:
		return "[dry-run] " + name
	}
}

func buildStructuredRepairRequest(raw string, schema StructuredOutputSchema, errors []string) map[string]any {
	encodedSchema, _ := json.Marshal(schema)
	return map[string]any{
		"messages": []map[string]any{
			{"role": "system", "content": "你只负责修复 JSON 格式。不得调用工具、不得写入数据、不补充事实；只返回符合契约的单个 JSON 对象，不要 Markdown。"},
			{"role": "user", "content": fmt.Sprintf("原始 final response：\n%s\n\n输出契约：\n%s\n\n校验错误：\n%s", raw, string(encodedSchema), strings.Join(errors, "\n"))},
		},
		"temperature":           0,
		"max_completion_tokens": 2048,
	}
}

// ProcessStructuredResponse accepts a full JSON document or one complete json fenced block.
// It intentionally never extracts a guessed JSON substring from a narrative response.
func ProcessStructuredResponse(raw string, schema StructuredOutputSchema) (map[string]any, StructuredOutputDiagnostic, error) {
	diagnostic := StructuredOutputDiagnostic{Status: structuredStatusError, Schema: schema}
	if err := validateStructuredSchema(schema); err != nil {
		diagnostic.Error = err.Error()
		return nil, diagnostic, err
	}
	body, parsePath := structuredJSONDocument(raw)
	diagnostic.ParsePath = parsePath
	if len(body) == 0 || len(body) > maxStructuredResponseBytes {
		err := fmt.Errorf("结构化响应为空或超过 %d 字节限制", maxStructuredResponseBytes)
		diagnostic.Error = err.Error()
		return nil, diagnostic, err
	}
	value, err := decodeStructuredJSON(body)
	if err != nil {
		diagnostic.Error = err.Error()
		return nil, diagnostic, err
	}
	object, ok := value.(map[string]any)
	if !ok {
		err = fmt.Errorf("结构化响应顶层必须为 object")
		diagnostic.Error = err.Error()
		return nil, diagnostic, err
	}
	if err := validateStructuredValue(object, schema); err != nil {
		diagnostic.Error = err.Error()
		diagnostic.ErrorDetails = []string{err.Error()}
		return nil, diagnostic, err
	}
	diagnostic.Status = structuredStatusValid
	return object, diagnostic, nil
}

func structuredJSONDocument(raw string) ([]byte, string) {
	trimmed := strings.TrimSpace(raw)
	if matches := structuredFenceRE.FindStringSubmatch(trimmed); len(matches) == 2 {
		return []byte(strings.TrimSpace(matches[1])), "json_fence"
	}
	return []byte(trimmed), "direct"
}

func decodeStructuredJSON(body []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	value, err := decodeStructuredJSONValue(decoder, 0)
	if err != nil {
		return nil, fmt.Errorf("结构化 JSON 解析失败: %w", err)
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("结构化 JSON 只能包含一个完整文档")
		}
		return nil, fmt.Errorf("结构化 JSON 尾部非法: %w", err)
	}
	return value, nil
}

func decodeStructuredJSONValue(decoder *json.Decoder, depth int) (any, error) {
	if depth > maxStructuredJSONDepth {
		return nil, fmt.Errorf("结构化 JSON 嵌套超过 %d 层", maxStructuredJSONDepth)
	}
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	switch delimiter := token.(type) {
	case json.Delim:
		switch delimiter {
		case '{':
			out := map[string]any{}
			for decoder.More() {
				nameToken, err := decoder.Token()
				if err != nil {
					return nil, err
				}
				name, ok := nameToken.(string)
				if !ok {
					return nil, fmt.Errorf("对象字段名必须是字符串")
				}
				if _, exists := out[name]; exists {
					return nil, fmt.Errorf("结构化 JSON 包含重复字段 %q", name)
				}
				value, err := decodeStructuredJSONValue(decoder, depth+1)
				if err != nil {
					return nil, err
				}
				out[name] = value
			}
			if end, err := decoder.Token(); err != nil || end != json.Delim('}') {
				return nil, fmt.Errorf("对象未正确结束")
			}
			return out, nil
		case '[':
			out := []any{}
			for decoder.More() {
				value, err := decodeStructuredJSONValue(decoder, depth+1)
				if err != nil {
					return nil, err
				}
				out = append(out, value)
			}
			if end, err := decoder.Token(); err != nil || end != json.Delim(']') {
				return nil, fmt.Errorf("数组未正确结束")
			}
			return out, nil
		default:
			return nil, fmt.Errorf("意外的 JSON 分隔符 %q", delimiter)
		}
	default:
		return token, nil
	}
}

func validateStructuredValue(value map[string]any, schema StructuredOutputSchema) error {
	fields := make(map[string]StructuredOutputField, len(schema.Fields))
	for _, field := range schema.Fields {
		fields[field.Name] = field
	}
	for name := range value {
		if _, known := fields[name]; !known {
			return fmt.Errorf("结构化响应包含未声明字段 %q", name)
		}
	}
	for _, field := range schema.Fields {
		value, exists := value[field.Name]
		if !exists {
			if field.Required {
				return fmt.Errorf("%s is required", field.Name)
			}
			continue
		}
		if err := validateStructuredFieldValue(field, value); err != nil {
			return err
		}
	}
	return nil
}

func validateStructuredFieldValue(field StructuredOutputField, value any) error {
	typeName := strings.ToLower(strings.TrimSpace(field.Type))
	if typeName == "array" {
		items, ok := value.([]any)
		if !ok {
			return fmt.Errorf("%s must be an array", field.Name)
		}
		for _, item := range items {
			if err := validateStructuredPrimitive(field.Name, field.Items.Type, item); err != nil {
				return err
			}
		}
		return nil
	}
	if typeName == "enum" {
		text, ok := value.(string)
		if !ok {
			return fmt.Errorf("%s must be an enum string", field.Name)
		}
		for _, allowed := range field.Enum {
			if text == allowed {
				return nil
			}
		}
		return fmt.Errorf("%s must be one of %s", field.Name, strings.Join(field.Enum, ", "))
	}
	if err := validateStructuredPrimitive(field.Name, typeName, value); err != nil {
		return err
	}
	if typeName == "string" && field.MaxLength > 0 && utf8.RuneCountInString(value.(string)) > field.MaxLength {
		return fmt.Errorf("%s exceeds max_length %d", field.Name, field.MaxLength)
	}
	return nil
}

func validateStructuredPrimitive(name, typeName string, value any) error {
	switch strings.ToLower(strings.TrimSpace(typeName)) {
	case "string":
		if _, ok := value.(string); !ok {
			return fmt.Errorf("%s must be a string", name)
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("%s must be a boolean", name)
		}
	case "number":
		if _, ok := value.(json.Number); !ok {
			return fmt.Errorf("%s must be a number", name)
		}
	case "integer":
		number, ok := value.(json.Number)
		if !ok {
			return fmt.Errorf("%s must be an integer", name)
		}
		if _, err := strconv.ParseInt(string(number), 10, 64); err != nil {
			return fmt.Errorf("%s must be an integer", name)
		}
	default:
		return fmt.Errorf("%s has unsupported type %q", name, typeName)
	}
	return nil
}
