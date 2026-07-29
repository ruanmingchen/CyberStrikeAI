package multiagent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"cyberstrike-ai/internal/config"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/prebuilt/planexecute"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

func TestV09InterruptRemainsResumable(t *testing.T) {
	t.Run("detects_interrupt_continue_cause", func(t *testing.T) {
		ctx, cancel := context.WithCancelCause(context.Background())
		cancel(ErrInterruptContinue)
		if !isInterruptContinue(ctx) {
			t.Fatal("expected isInterruptContinue for ErrInterruptContinue cause")
		}
		if !errors.Is(context.Cause(ctx), ErrInterruptContinue) {
			t.Fatal("cause should be ErrInterruptContinue")
		}
	})

	t.Run("plain_cancel_is_not_interrupt_continue", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if isInterruptContinue(ctx) {
			t.Fatal("plain context.Canceled must not be interrupt-continue")
		}
	})

	t.Run("checkpoint_survives_interrupt_for_all_modes", func(t *testing.T) {
		dir := t.TempDir()
		store, err := newFileCheckPointStore(dir)
		if err != nil {
			t.Fatalf("store: %v", err)
		}
		for _, mode := range []string{"deep", "supervisor", "plan_execute"} {
			id := buildEinoCheckpointID(mode)
			if err := store.Set(context.Background(), id, []byte("ckpt-"+mode)); err != nil {
				t.Fatalf("Set %s: %v", mode, err)
			}
			// Mirror run-loop: on ctx cancel / interrupt, checkpoint is kept.
			got, ok, gErr := store.Get(context.Background(), id)
			if gErr != nil || !ok {
				t.Fatalf("Get %s: ok=%v err=%v", mode, ok, gErr)
			}
			if string(got) != "ckpt-"+mode {
				t.Fatalf("%s payload mismatch: %q", mode, got)
			}
			if !strings.HasPrefix(id, "runner-v09-") {
				t.Fatalf("expected v09 namespace, got %s", id)
			}
		}
	})
}

func TestV09DeepAgentDelegation(t *testing.T) {
	t.Run("nested_task_forbidden", func(t *testing.T) {
		mw := newNoNestedTaskMiddleware()
		outer, err := mw.WrapInvokableToolCall(context.Background(),
			func(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
				inner, werr := mw.WrapInvokableToolCall(ctx,
					func(context.Context, string, ...tool.Option) (string, error) {
						return "should-not-run", nil
					},
					&adk.ToolContext{Name: "task"},
				)
				if werr != nil {
					return "", werr
				}
				return inner(ctx, `{"description":"nested"}`)
			},
			&adk.ToolContext{Name: "task"},
		)
		if err != nil {
			t.Fatalf("outer wrap: %v", err)
		}
		out, err := outer(context.Background(), `{"description":"parent"}`)
		if err != nil {
			t.Fatalf("outer invoke: %v", err)
		}
		if !strings.Contains(out, "Nested task delegation is forbidden") {
			t.Fatalf("expected nested forbid message, got %q", out)
		}
	})

	t.Run("hitl_exempt_includes_task", func(t *testing.T) {
		merged := MergeHitlExemptMetaTools(nil)
		found := false
		for _, name := range merged {
			if strings.EqualFold(name, "task") {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("task missing from HITL exempt list: %v", merged)
		}
	})

	t.Run("checkpoint_namespace_deep", func(t *testing.T) {
		if got := buildEinoCheckpointID("deep"); got != "runner-v09-deep" {
			t.Fatalf("got %s", got)
		}
	})
}

func TestV09SupervisorCompatibility(t *testing.T) {
	t.Run("checkpoint_namespace", func(t *testing.T) {
		if got := buildEinoCheckpointID("supervisor"); got != "runner-v09-supervisor" {
			t.Fatalf("got %s", got)
		}
	})

	t.Run("orchestration_normalize", func(t *testing.T) {
		if got := config.NormalizeMultiAgentOrchestration("Supervisor"); got != "supervisor" {
			t.Fatalf("got %s", got)
		}
	})

	t.Run("hitl_exempt_transfer_and_exit", func(t *testing.T) {
		merged := MergeHitlExemptMetaTools(nil)
		need := map[string]bool{"transfer_to_agent": false, "exit": false}
		for _, name := range merged {
			n := strings.ToLower(strings.TrimSpace(name))
			if _, ok := need[n]; ok {
				need[n] = true
			}
		}
		for k, ok := range need {
			if !ok {
				t.Fatalf("%s missing from HITL exempt: %v", k, merged)
			}
		}
	})

	t.Run("cancel_error_not_business_failure", func(t *testing.T) {
		err := &adk.CancelError{Info: &adk.AgentCancelInfo{}}
		var cancelErr *adk.CancelError
		if !errors.As(err, &cancelErr) {
			t.Fatal("expected CancelError")
		}
	})
}

func TestV09PlanExecuteSessionContract(t *testing.T) {
	t.Run("session_key_constants", func(t *testing.T) {
		if planexecute.PlanSessionKey == "" || planexecute.UserInputSessionKey == "" ||
			planexecute.ExecutedStepSessionKey == "" || planexecute.ExecutedStepsSessionKey == "" {
			t.Fatal("planexecute session keys must be non-empty")
		}
		if buildEinoCheckpointID("plan_execute") != "runner-v09-plan_execute" {
			t.Fatalf("unexpected pe checkpoint id: %s", buildEinoCheckpointID("plan_execute"))
		}
	})

	t.Run("missing_plan_errors", func(t *testing.T) {
		_, err := loadPlanExecuteExecutorContext(context.Background())
		if err == nil || !strings.Contains(err.Error(), planexecute.PlanSessionKey) {
			t.Fatalf("expected missing Plan error, got %v", err)
		}
	})

	t.Run("valid_execution_context_builds_executor_input", func(t *testing.T) {
		in := &planexecute.ExecutionContext{
			UserInput: []adk.Message{schema.UserMessage("scan target.example")},
			Plan:      &lenientPlan{Steps: []string{"enumerate ports", "probe service"}},
			ExecutedSteps: []planexecute.ExecutedStep{
				{Step: "enumerate ports", Result: "open:22,80"},
			},
		}
		if in.Plan.FirstStep() != "enumerate ports" {
			t.Fatalf("FirstStep=%q", in.Plan.FirstStep())
		}
		msgs, err := planExecuteExecutorGenInput("orch-sys", nil, nil, nil, "gpt-test", "conv-1")(context.Background(), in)
		if err != nil {
			t.Fatalf("genInput: %v", err)
		}
		if len(msgs) == 0 {
			t.Fatal("expected non-empty executor model input")
		}
		joined := ""
		sysCount := 0
		for _, m := range msgs {
			if m == nil {
				continue
			}
			joined += m.Content
			if m.Role == schema.System {
				sysCount++
			}
		}
		if !strings.Contains(joined, "enumerate ports") && !strings.Contains(joined, "probe service") {
			t.Fatalf("expected plan content in input: %s", joined)
		}
		if sysCount > 1 {
			t.Fatalf("expected at most one system message, got %d", sysCount)
		}
	})

	t.Run("session_roundtrip_via_runner", func(t *testing.T) {
		ctx := context.Background()
		stub := &v09StubToolCallingModel{reply: "step-result"}
		exec, err := newPlanExecuteExecutor(ctx, &planexecute.ExecutorConfig{
			Model: stub,
			GenInputFn: func(ctx context.Context, in *planexecute.ExecutionContext) ([]adk.Message, error) {
				if in == nil || in.Plan == nil {
					t.Fatal("expected loaded ExecutionContext")
				}
				if in.Plan.FirstStep() != "step-a" {
					t.Fatalf("FirstStep=%q", in.Plan.FirstStep())
				}
				if len(in.UserInput) != 1 {
					t.Fatalf("UserInput len=%d", len(in.UserInput))
				}
				return []adk.Message{schema.UserMessage("exec-now")}, nil
			},
		}, nil)
		if err != nil {
			t.Fatalf("newPlanExecuteExecutor: %v", err)
		}
		runner := adk.NewRunner(ctx, adk.RunnerConfig{Agent: exec})
		iter := runner.Run(ctx, []adk.Message{schema.UserMessage("noop")},
			adk.WithSessionValues(map[string]any{
				planexecute.PlanSessionKey:      planexecute.Plan(&lenientPlan{Steps: []string{"step-a"}}),
				planexecute.UserInputSessionKey: []adk.Message{schema.UserMessage("target")},
			}),
		)
		ev, ok := iter.Next()
		if !ok {
			t.Fatal("expected executor event")
		}
		if ev != nil && ev.Err != nil {
			t.Fatalf("executor event error: %v", ev.Err)
		}
	})

	t.Run("wrong_plan_type_via_runner", func(t *testing.T) {
		ctx := context.Background()
		stub := &v09StubToolCallingModel{reply: "unused"}
		exec, err := newPlanExecuteExecutor(ctx, &planexecute.ExecutorConfig{Model: stub}, nil)
		if err != nil {
			t.Fatalf("executor: %v", err)
		}
		runner := adk.NewRunner(ctx, adk.RunnerConfig{Agent: exec})
		iter := runner.Run(ctx, []adk.Message{schema.UserMessage("noop")},
			adk.WithSessionValues(map[string]any{
				planexecute.PlanSessionKey:      "not-a-plan",
				planexecute.UserInputSessionKey: []adk.Message{schema.UserMessage("x")},
			}),
		)
		ev, ok := iter.Next()
		if !ok || ev == nil || ev.Err == nil {
			t.Fatal("expected session type error event")
		}
		if !strings.Contains(ev.Err.Error(), "invalid type") {
			t.Fatalf("expected invalid type, got %v", ev.Err)
		}
	})

	t.Run("executor_output_key_constant", func(t *testing.T) {
		if planexecute.ExecutedStepSessionKey != "ExecutedStep" {
			t.Fatalf("unexpected ExecutedStepSessionKey=%q", planexecute.ExecutedStepSessionKey)
		}
	})

	t.Run("nil_executor_config", func(t *testing.T) {
		if _, err := newPlanExecuteExecutor(context.Background(), nil, nil); err == nil {
			t.Fatal("expected nil config error")
		}
		if _, err := newPlanExecuteExecutor(context.Background(), &planexecute.ExecutorConfig{}, nil); err == nil {
			t.Fatal("expected nil model error")
		}
	})
}

// v09StubToolCallingModel is a minimal ToolCallingChatModel for PlanExecute session tests.
type v09StubToolCallingModel struct {
	reply string
}

func (m *v09StubToolCallingModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	_ = ctx
	_ = input
	_ = opts
	return schema.AssistantMessage(m.reply, nil), nil
}

func (m *v09StubToolCallingModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	_ = ctx
	_ = input
	_ = opts
	return nil, errors.New("stream not supported in stub")
}

func (m *v09StubToolCallingModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	_ = tools
	return m, nil
}
