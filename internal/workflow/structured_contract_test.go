package workflow

import (
	"strings"
	"testing"
)

func testSurfaceSchema() StructuredOutputSchema {
	return StructuredOutputSchema{
		ContractVersion: 1,
		Type:            "object",
		Fields: []StructuredOutputField{
			{Name: "decision", Type: "enum", Required: true, Description: "route", Enum: []string{"proceed", "no_finding", "insufficient_evidence", "needs_review"}},
			{Name: "has_attack_surface", Type: "boolean", Required: true, Description: "evidence supports an attack surface"},
			{Name: "summary", Type: "string", Required: true, Description: "human summary", MaxLength: 2000},
			{Name: "evidence", Type: "array", Description: "evidence", Items: &StructuredOutputItems{Type: "string"}},
		},
	}
}

func TestProcessStructuredResponseAcceptsDirectJSONAndSingleFence(t *testing.T) {
	schema := testSurfaceSchema()
	for _, raw := range []string{
		`{"decision":"proceed","has_attack_surface":true,"summary":"public API","evidence":["/api"]}`,
		"```json\n{\"decision\":\"proceed\",\"has_attack_surface\":true,\"summary\":\"public API\",\"evidence\":[\"/api\"]}\n```",
	} {
		got, diagnostic, err := ProcessStructuredResponse(raw, schema)
		if err != nil {
			t.Fatalf("ProcessStructuredResponse(%q): %v", raw, err)
		}
		if diagnostic.Status != structuredStatusValid {
			t.Fatalf("status = %q, want %q", diagnostic.Status, structuredStatusValid)
		}
		if got["decision"] != "proceed" || got["has_attack_surface"] != true {
			t.Fatalf("unexpected structured value: %#v", got)
		}
	}
}

func TestProcessStructuredResponseRejectsNarrativeUnknownFieldsAndTopLevelArray(t *testing.T) {
	schema := testSurfaceSchema()
	for _, raw := range []string{
		`model explanation {"decision":"proceed","has_attack_surface":true,"summary":"x"}`,
		`{"decision":"proceed","has_attack_surface":true,"summary":"x","extra":"no"}`,
		`[{"decision":"proceed"}]`,
	} {
		_, _, err := ProcessStructuredResponse(raw, schema)
		if err == nil {
			t.Fatalf("ProcessStructuredResponse(%q) unexpectedly succeeded", raw)
		}
	}
}

func TestParseStructuredOutputContractDefaultsToTextAndRejectsInvalidRepair(t *testing.T) {
	contract, err := parseStructuredOutputContract(map[string]any{})
	if err != nil {
		t.Fatalf("parse default contract: %v", err)
	}
	if contract.Mode != structuredOutputModeText {
		t.Fatalf("default output mode = %q, want %q", contract.Mode, structuredOutputModeText)
	}
	_, err = parseStructuredOutputContract(map[string]any{
		"output_mode":   "json_schema",
		"output_schema": testSurfaceSchema(),
		"structured_output": map[string]any{
			"repair_attempts": 2,
			"failure_policy":  "route",
		},
	})
	if err == nil || !strings.Contains(err.Error(), "repair_attempts") {
		t.Fatalf("invalid repair attempts error = %v", err)
	}
}

func TestValidateGraphRejectsInvalidStructuredAgentContract(t *testing.T) {
	graph := `{
		"nodes": [
			{"id":"start","type":"start","config":{}},
			{"id":"agent","type":"agent","config":{
				"instruction":"analyze", "output_key":"surface_result", "output_mode":"json_schema",
				"output_schema":{"contract_version":1,"type":"object","fields":[
					{"name":"decision","type":"enum","required":true,"description":"route","enum":["proceed","no_finding"]}
				]},
				"structured_output":{"repair_attempts":2,"failure_policy":"route"}
			}},
			{"id":"output","type":"output","config":{"output_key":"result"}}
		],
		"edges": [{"source":"start","target":"agent"},{"source":"agent","target":"output"}]
	}`
	g, err := parseGraph(graph)
	if err != nil {
		t.Fatalf("parse graph: %v", err)
	}
	err = validateGraphDefinition(g, indexGraph(g))
	if err == nil || !strings.Contains(err.Error(), "repair_attempts") {
		t.Fatalf("validate graph error = %v, want repair_attempts", err)
	}
}

func TestValidateGraphRejectsUnknownStructuredOutputFieldReference(t *testing.T) {
	graph := `{
		"nodes": [
			{"id":"start","type":"start","config":{}},
			{"id":"agent","type":"agent","config":{"instruction":"analyze","output_key":"surface_result","output_mode":"json_schema","output_schema":{"contract_version":1,"type":"object","fields":[{"name":"decision","type":"enum","required":true,"description":"route","enum":["proceed"]}]}}},
			{"id":"output","type":"output","config":{"output_key":"result","source_binding":{"from":"outputs","field":"surface_result.missing"}}}
		], "edges":[{"source":"start","target":"agent"},{"source":"agent","target":"output"}]}`
	g, err := parseGraph(graph)
	if err != nil {
		t.Fatalf("parse graph: %v", err)
	}
	err = validateGraphDefinition(g, indexGraph(g))
	if err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("validate graph error = %v, want missing field", err)
	}
}

func TestApplyStructuredAgentResultStoresObjectAndRoutesFormatFailure(t *testing.T) {
	node := graphNode{ID: "agent-1", Type: "agent", Config: map[string]any{
		"output_key":    "surface_result",
		"output_mode":   "json_schema",
		"output_schema": testSurfaceSchema(),
		"structured_output": map[string]any{
			"repair_attempts": 0,
			"failure_policy":  "route",
		},
	}}
	state := newWorkflowLocalState(nil, "run")
	valid := `{"decision":"proceed","has_attack_surface":true,"summary":"public API","evidence":["/api"]}`
	out, proceed, status, errText := applyStructuredAgentResult(node, state, valid, "eino_single", nil)
	if !proceed || status != "completed" || errText != "" {
		t.Fatalf("valid result = (%v, %q, %q)", proceed, status, errText)
	}
	if out["structured_status"] != structuredStatusValid {
		t.Fatalf("valid status = %#v", out)
	}
	if _, ok := state.Outputs["surface_result"].(map[string]any); !ok {
		t.Fatalf("structured output = %#v, want object", state.Outputs["surface_result"])
	}

	state = newWorkflowLocalState(nil, "run")
	out, proceed, status, errText = applyStructuredAgentResult(node, state, "not JSON", "eino_single", nil)
	if !proceed || status != "completed" || errText != "" {
		t.Fatalf("route failure = (%v, %q, %q)", proceed, status, errText)
	}
	if out["structured_status"] != structuredStatusError {
		t.Fatalf("route status = %#v", out)
	}
	if _, exists := state.Outputs["surface_result"]; exists {
		t.Fatalf("route failure must not write output: %#v", state.Outputs)
	}
}

func TestDryRunStructuredAgentPublishesSchemaValidObject(t *testing.T) {
	graph := `{
		"nodes": [
			{"id":"start","type":"start","config":{}},
			{"id":"agent","type":"agent","config":{
				"instruction":"analyze", "output_key":"surface_result", "output_mode":"json_schema",
				"output_schema":{"contract_version":1,"type":"object","fields":[
					{"name":"decision","type":"enum","required":true,"description":"route","enum":["proceed","no_finding"]},
					{"name":"has_attack_surface","type":"boolean","required":true,"description":"attack surface"}
				]}
			}},
			{"id":"output","type":"output","config":{"output_key":"result","source_binding":{"from":"outputs","field":"surface_result.has_attack_surface"}}}
		],
		"edges": [{"source":"start","target":"agent"},{"source":"agent","target":"output"}]
	}`
	result, err := DryRunGraphJSON(t.Context(), graph, nil)
	if err != nil {
		t.Fatalf("DryRunGraphJSON: %v", err)
	}
	value, ok := result.Outputs["surface_result"].(map[string]any)
	if !ok || value["has_attack_surface"] != true {
		t.Fatalf("structured dry-run output = %#v", result.Outputs["surface_result"])
	}
	if result.NodeOutputs["agent"]["structured_status"] != structuredStatusValid {
		t.Fatalf("agent node output = %#v", result.NodeOutputs["agent"])
	}
	if result.Outputs["result"] != true {
		t.Fatalf("nested source binding result = %#v", result.Outputs["result"])
	}
}

func TestBuildStructuredRepairRequestHasNoToolsAndPreservesContractOnly(t *testing.T) {
	payload := buildStructuredRepairRequest("not json", testSurfaceSchema(), []string{"decision is required"})
	if _, exists := payload["tools"]; exists {
		t.Fatalf("repair request must not expose tools: %#v", payload)
	}
	messages, ok := payload["messages"].([]map[string]any)
	if !ok || len(messages) != 2 {
		t.Fatalf("repair messages = %#v", payload["messages"])
	}
	if !strings.Contains(messages[0]["content"].(string), "不补充事实") {
		t.Fatalf("repair system message = %#v", messages[0])
	}
	if strings.Contains(messages[1]["content"].(string), "MCP") {
		t.Fatalf("repair prompt must not add agent/tool context: %#v", messages[1])
	}
}
