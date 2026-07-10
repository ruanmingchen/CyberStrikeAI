package multiagent

import (
	"context"
	"strings"
	"testing"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

func TestModelInputBudgetDropsOldestCompleteRoundsAndPersistsLatest(t *testing.T) {
	mw := &modelInputBudgetMiddleware{
		maxTokens: 7,
		counter:   fixedTokenCounter(4),
		phase:     "test",
	}
	state := &adk.ChatModelAgentState{Messages: []adk.Message{
		schema.SystemMessage("system"),
		schema.UserMessage("old-user"),
		schema.AssistantMessage("old-answer", nil),
		schema.UserMessage("latest-user"),
		assistantToolCallsMsg("", "latest-call"),
		schema.ToolMessage("latest-result", "latest-call"),
	}}
	_, out, err := mw.BeforeModelRewriteState(context.Background(), state, nil)
	if err != nil {
		t.Fatal(err)
	}
	joined := formatSummarizationTranscript(out.Messages)
	if strings.Contains(joined, "old-user") || strings.Contains(joined, "old-answer") {
		t.Fatalf("old rounds retained after hard budget: %s", joined)
	}
	if !strings.Contains(joined, "latest-user") || !strings.Contains(joined, "latest-result") {
		t.Fatalf("latest rounds lost after hard budget: %s", joined)
	}
}

func TestModelInputBudgetFailsLocallyWhenLatestRoundAloneCannotFit(t *testing.T) {
	mw := &modelInputBudgetMiddleware{maxTokens: 2, counter: fixedTokenCounter(4), phase: "test"}
	state := &adk.ChatModelAgentState{Messages: []adk.Message{
		schema.SystemMessage("system"),
		assistantToolCallsMsg("", "latest-call"),
		schema.ToolMessage("latest-result", "latest-call"),
	}}
	_, _, err := mw.BeforeModelRewriteState(context.Background(), state, nil)
	if err == nil || !strings.Contains(err.Error(), "hard budget") {
		t.Fatalf("expected local hard-budget error, got %v", err)
	}
}
