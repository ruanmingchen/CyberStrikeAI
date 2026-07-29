package multiagent

import (
	"bytes"
	"context"
	"encoding/gob"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
	"github.com/eino-contrib/jsonschema"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

func TestV09TelemetryUsesStateToolInfos(t *testing.T) {
	core, logs := observer.New(zapcore.InfoLevel)
	logger := zap.New(core)
	mw := newEinoModelInputTelemetryMiddleware(logger, "gpt-test", "conv-1", "phase")
	if mw == nil {
		t.Fatal("expected telemetry middleware")
	}

	stateTools := []*schema.ToolInfo{
		{Name: "from_state_a"},
		{Name: "from_state_b"},
	}
	mcToolsOnly := []*schema.ToolInfo{{Name: "from_mc_only"}}

	state := &adk.ChatModelAgentState{
		Messages:  []adk.Message{schema.UserMessage("hi")},
		ToolInfos: stateTools,
	}
	mc := &adk.ModelContext{Tools: mcToolsOnly}

	if _, _, err := mw.BeforeModelRewriteState(context.Background(), state, mc); err != nil {
		t.Fatalf("BeforeModelRewriteState: %v", err)
	}
	got := stateToolInfos(state, mc)
	if len(got) != 2 || got[0].Name != "from_state_a" {
		t.Fatalf("expected state.ToolInfos preferred, got %#v", got)
	}

	emptyState := &adk.ChatModelAgentState{Messages: []adk.Message{schema.UserMessage("hi")}}
	fallback := stateToolInfos(emptyState, mc)
	if len(fallback) != 1 || fallback[0].Name != "from_mc_only" {
		t.Fatalf("expected ModelContext.Tools fallback, got %#v", fallback)
	}

	if logs.Len() == 0 {
		t.Fatal("expected telemetry log")
	}
	entry := logs.All()[0]
	switch v := entry.ContextMap()["tools"].(type) {
	case int:
		if v != 2 {
			t.Fatalf("expected tools=2, got %d", v)
		}
	case int64:
		if v != 2 {
			t.Fatalf("expected tools=2, got %d", v)
		}
	default:
		t.Fatalf("expected tools field, got %#v", entry.ContextMap()["tools"])
	}
}

func TestV09CheckpointNamespaceDoesNotResumeV08(t *testing.T) {
	id := buildEinoCheckpointID("deep")
	if id != "runner-v09-deep" {
		t.Fatalf("unexpected checkpoint id: %s", id)
	}
	if id == "runner-deep" {
		t.Fatal("v0.9 checkpoint id must differ from v0.8")
	}
}

func TestV09CancelErrorIsNotReportedAsFailure(t *testing.T) {
	err := &adk.CancelError{Info: &adk.AgentCancelInfo{}}
	var cancelErr *adk.CancelError
	if !errors.As(err, &cancelErr) {
		t.Fatal("expected CancelError detection")
	}
	if errors.Is(err, context.Canceled) {
		t.Fatal("CancelError should not be context.Canceled")
	}
	kind := "error"
	if errors.As(err, &cancelErr) {
		kind = "cancel"
	} else if errors.Is(err, context.Canceled) {
		kind = "context_canceled"
	}
	if kind != "cancel" {
		t.Fatalf("expected cancel kind, got %s", kind)
	}
}

func TestV09ToolInfoSurvivesCheckpointResume(t *testing.T) {
	dir := t.TempDir()
	store, err := newFileCheckPointStore(dir)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	js := jsonschema.Schema{Type: string(schema.Object)}
	info := &schema.ToolInfo{
		Name:        "echo_tool",
		Desc:        "echo",
		ParamsOneOf: schema.NewParamsOneOfByJSONSchema(&js),
	}

	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(info); err != nil {
		t.Fatalf("gob encode ToolInfo: %v", err)
	}

	id := buildEinoCheckpointID("deep")
	if err := store.Set(context.Background(), id, buf.Bytes()); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, ok, gErr := store.Get(context.Background(), id)
	if gErr != nil || !ok {
		t.Fatalf("Get: ok=%v err=%v", ok, gErr)
	}
	var decoded schema.ToolInfo
	if err := gob.NewDecoder(bytes.NewReader(got)).Decode(&decoded); err != nil {
		t.Fatalf("gob decode ToolInfo: %v", err)
	}
	if decoded.Name != "echo_tool" || decoded.ParamsOneOf == nil {
		t.Fatalf("ParamsOneOf lost after checkpoint: %#v", decoded)
	}

	// JSON round-trip also preserves ParamsOneOf (V0.9 explicit codec).
	jb, err := info.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}
	var jDecoded schema.ToolInfo
	if err := jDecoded.UnmarshalJSON(jb); err != nil {
		t.Fatalf("UnmarshalJSON: %v", err)
	}
	if jDecoded.Name != "echo_tool" || jDecoded.ParamsOneOf == nil {
		t.Fatalf("ParamsOneOf lost after JSON: %#v", jDecoded)
	}

	v08Path := filepath.Join(dir, "runner-deep.ckpt")
	v09Path := filepath.Join(dir, id+".ckpt")
	if v08Path == v09Path {
		t.Fatal("v08/v09 checkpoint paths collided")
	}
}

func TestHitlRejectToolResultDoesNotUnlockTools(t *testing.T) {
	body := HitlRejectToolResult("nmap", "no")
	if !strings.Contains(body, "HITL Reject") {
		t.Fatalf("expected reject body: %s", body)
	}
	tsBody := HitlRejectToolResult("tool_search", "denied")
	if !strings.Contains(tsBody, `"selectedTools":[]`) && !strings.Contains(tsBody, `"selectedTools": []`) {
		t.Fatalf("tool_search reject must keep empty selectedTools JSON: %s", tsBody)
	}
	if !strings.Contains(tsBody, "hitlRejected") {
		t.Fatalf("expected hitl rejected marker: %s", tsBody)
	}
}
