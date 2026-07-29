package multiagent

import (
	"context"
	"strings"
	"testing"

	"cyberstrike-ai/internal/config"

	"github.com/cloudwego/eino/schema"
)

func TestV09SummarizationFinalizePreservesConfiguredContext(t *testing.T) {
	original := []*schema.Message{
		schema.SystemMessage("sys-base"),
		schema.UserMessage("user-goal-scan example.com"),
		schema.AssistantMessage("ack", nil),
		schema.UserMessage("follow-up-port-scan"),
	}
	raw := schema.AssistantMessage(`<analysis>discard-me</analysis>
<summary>
## progress
working
<all_user_messages>
    - placeholder
</all_user_messages>
</summary>`, nil)

	out, err := finalizeSummarizationForV09(context.Background(), original, raw, finalizeSummarizationOpts{
		tokenCounter:            fixedTokenCounter(1),
		recentTrailMax:          8,
		userLedgerMaxRunes:      config.DefaultSummarizationUserIntentLedgerMaxRunes,
		userLedgerEntryMaxRunes: config.DefaultSummarizationUserIntentLedgerEntryMaxRunes,
	})
	if err != nil {
		t.Fatalf("finalize: %v", err)
	}
	joined := joinMessageContents(out)
	if strings.Contains(joined, "discard-me") {
		t.Fatalf("analysis should be stripped: %s", joined)
	}
	if !strings.Contains(joined, "user-goal-scan example.com") {
		t.Fatalf("expected preserved user messages in finalized summary: %s", joined)
	}
	if !strings.Contains(joined, "follow-up-port-scan") {
		t.Fatalf("expected later user message preserved: %s", joined)
	}
}

func TestV09SummarizationKeepsTranscriptReference(t *testing.T) {
	path := "/tmp/eino-v09-transcript-test.jsonl"
	original := []*schema.Message{
		schema.SystemMessage("sys"),
		schema.UserMessage("hello"),
	}
	raw := schema.AssistantMessage(`<summary>done
<all_user_messages>
    - hello
</all_user_messages>
</summary>`, nil)

	out, err := finalizeSummarizationForV09(context.Background(), original, raw, finalizeSummarizationOpts{
		transcriptPath:          path,
		tokenCounter:            fixedTokenCounter(1),
		recentTrailMax:          0,
		userLedgerMaxRunes:      config.DefaultSummarizationUserIntentLedgerMaxRunes,
		userLedgerEntryMaxRunes: config.DefaultSummarizationUserIntentLedgerEntryMaxRunes,
	})
	if err != nil {
		t.Fatalf("finalize: %v", err)
	}
	joined := joinMessageContents(out)
	if !strings.Contains(joined, path) {
		t.Fatalf("expected transcript path reminder, got: %s", joined)
	}
}

func TestV09SummarizationDoesNotDuplicateUserLedger(t *testing.T) {
	ledger := buildOriginalUserIntentLedgerMessage(
		[]*schema.Message{schema.UserMessage("intent-a")},
		config.DefaultSummarizationUserIntentLedgerMaxRunes,
		config.DefaultSummarizationUserIntentLedgerEntryMaxRunes,
	)
	if ledger == nil {
		t.Fatal("expected ledger")
	}
	original := []*schema.Message{
		schema.SystemMessage("sys\n\n" + ledger.Content),
		schema.UserMessage("intent-a"),
		schema.AssistantMessage("ok", nil),
	}
	raw := schema.AssistantMessage(`<summary>compact
<all_user_messages>
    - intent-a
</all_user_messages>
</summary>`, nil)

	out, err := finalizeSummarizationForV09(context.Background(), original, raw, finalizeSummarizationOpts{
		tokenCounter:            fixedTokenCounter(1),
		recentTrailMax:          4,
		userLedgerMaxRunes:      config.DefaultSummarizationUserIntentLedgerMaxRunes,
		userLedgerEntryMaxRunes: config.DefaultSummarizationUserIntentLedgerEntryMaxRunes,
	})
	if err != nil {
		t.Fatalf("finalize: %v", err)
	}
	joined := joinMessageContents(out)
	if strings.Count(joined, "<original_user_intent_ledger>") != 1 {
		t.Fatalf("user intent ledger must appear exactly once: %s", joined)
	}
	if strings.Count(joined, "[U001] intent-a") != 1 {
		t.Fatalf("ledger entry must not be duplicated: %s", joined)
	}
}

func TestV09SummarizationKeepsRecentAssistantToolTrail(t *testing.T) {
	original := []*schema.Message{
		schema.SystemMessage("sys"),
		schema.UserMessage("run tool"),
		assistantToolCallsMsg("", "call-1"),
		schema.ToolMessage("tool-result-payload", "call-1"),
	}
	raw := schema.AssistantMessage(`<summary>summary-body
<all_user_messages>
    - run tool
</all_user_messages>
</summary>`, nil)

	out, err := finalizeSummarizationForV09(context.Background(), original, raw, finalizeSummarizationOpts{
		tokenCounter:            fixedTokenCounter(1),
		recentTrailMax:          8,
		userLedgerMaxRunes:      config.DefaultSummarizationUserIntentLedgerMaxRunes,
		userLedgerEntryMaxRunes: config.DefaultSummarizationUserIntentLedgerEntryMaxRunes,
	})
	if err != nil {
		t.Fatalf("finalize: %v", err)
	}
	joined := joinMessageContents(out)
	if !strings.Contains(joined, "tool-result-payload") {
		t.Fatalf("expected recent tool trail preserved: %s", joined)
	}
	hasAssistantTC := false
	for _, m := range out {
		if m != nil && m.Role == schema.Assistant && len(m.ToolCalls) > 0 {
			hasAssistantTC = true
		}
	}
	if !hasAssistantTC {
		t.Fatalf("expected assistant tool_calls in trail: %+v", out)
	}
}

func TestV09SummarizationProducesSingleSystemMessage(t *testing.T) {
	original := []*schema.Message{
		schema.SystemMessage("sys-a"),
		schema.SystemMessage("sys-b"),
		schema.UserMessage("u1"),
	}
	raw := schema.AssistantMessage(`<summary>s
<all_user_messages>
    - u1
</all_user_messages>
</summary>`, nil)

	out, err := finalizeSummarizationForV09(context.Background(), original, raw, finalizeSummarizationOpts{
		tokenCounter:            fixedTokenCounter(1),
		recentTrailMax:          4,
		userLedgerMaxRunes:      config.DefaultSummarizationUserIntentLedgerMaxRunes,
		userLedgerEntryMaxRunes: config.DefaultSummarizationUserIntentLedgerEntryMaxRunes,
	})
	if err != nil {
		t.Fatalf("finalize: %v", err)
	}
	sysCount := 0
	for _, m := range out {
		if m != nil && m.Role == schema.System {
			sysCount++
		}
	}
	if sysCount != 1 {
		t.Fatalf("expected exactly one system message, got %d in %+v", sysCount, rolesOf(out))
	}
}

func joinMessageContents(msgs []*schema.Message) string {
	var b strings.Builder
	for _, m := range msgs {
		if m == nil {
			continue
		}
		b.WriteString(m.Content)
		b.WriteByte('\n')
	}
	return b.String()
}

func rolesOf(msgs []*schema.Message) []string {
	out := make([]string, 0, len(msgs))
	for _, m := range msgs {
		if m == nil {
			out = append(out, "<nil>")
			continue
		}
		out = append(out, string(m.Role))
	}
	return out
}
