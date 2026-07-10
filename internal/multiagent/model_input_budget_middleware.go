package multiagent

import (
	"context"
	"fmt"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/middlewares/summarization"
	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"
)

// modelInputBudgetMiddleware is the final deterministic guard before a normal model call.
// Summarization remains the primary compaction strategy; this middleware only removes the
// oldest complete rounds if the finalized state still exceeds 65% of the configured
// window, leaving serialization/tokenizer headroom for the outbound HTTP guard.
type modelInputBudgetMiddleware struct {
	adk.BaseChatModelAgentMiddleware
	maxTokens int
	counter   summarization.TokenCounterFunc
	logger    *zap.Logger
	phase     string
}

func newModelInputBudgetMiddleware(maxTotalTokens int, modelName string, logger *zap.Logger, phase string) adk.ChatModelAgentMiddleware {
	if maxTotalTokens <= 0 {
		maxTotalTokens = 120000
	}
	limit := int(float64(maxTotalTokens) * 0.65)
	if limit < 4096 {
		limit = 4096
	}
	return &modelInputBudgetMiddleware{
		maxTokens: limit,
		counter:   einoSummarizationTokenCounter(modelName),
		logger:    logger,
		phase:     phase,
	}
}

func (m *modelInputBudgetMiddleware) BeforeModelRewriteState(
	ctx context.Context,
	state *adk.ChatModelAgentState,
	mc *adk.ModelContext,
) (context.Context, *adk.ChatModelAgentState, error) {
	if m == nil || state == nil || len(state.Messages) == 0 {
		return ctx, state, nil
	}
	count := func(msgs []adk.Message) (int, error) {
		input := &summarization.TokenCounterInput{Messages: msgs}
		if mc != nil {
			input.Tools = mc.Tools
		}
		return m.counter(ctx, input)
	}
	before, err := count(state.Messages)
	if err != nil {
		return ctx, state, err
	}
	if before <= m.maxTokens {
		return ctx, state, nil
	}

	systems := make([]adk.Message, 0, 1)
	contextMsgs := make([]adk.Message, 0, len(state.Messages))
	for _, msg := range state.Messages {
		if msg != nil && msg.Role == schema.System && len(contextMsgs) == 0 {
			systems = append(systems, msg)
			continue
		}
		if msg != nil {
			contextMsgs = append(contextMsgs, msg)
		}
	}
	rounds := splitMessagesIntoRounds(contextMsgs)
	dropped := 0
	var candidate []adk.Message
	for len(rounds) > 1 {
		rounds = rounds[1:]
		dropped++
		candidate = append(candidate[:0], systems...)
		for _, round := range rounds {
			candidate = append(candidate, round.messages...)
		}
		after, countErr := count(candidate)
		if countErr != nil {
			return ctx, state, countErr
		}
		if after <= m.maxTokens {
			out := *state
			out.Messages = append([]adk.Message(nil), candidate...)
			if m.logger != nil {
				m.logger.Warn("eino model input hard budget applied",
					zap.String("phase", m.phase), zap.Int("tokens_before", before),
					zap.Int("tokens_after", after), zap.Int("max_tokens", m.maxTokens),
					zap.Int("dropped_rounds", dropped))
			}
			return ctx, &out, nil
		}
	}

	return ctx, state, fmt.Errorf(
		"model input exceeds configured hard budget after preserving the latest round: tokens=%d max=%d phase=%s",
		before, m.maxTokens, m.phase,
	)
}
