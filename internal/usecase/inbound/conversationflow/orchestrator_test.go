package conversationflow

import (
	"context"
	"strings"
	"testing"

	"assistant-api/internal/integration/unifiedmessage"
	llminteraction "assistant-api/internal/usecase/ai/llm_interaction"
)

type stubConversationLLM struct {
	answer        *llminteraction.QuestionAnswer
	clarify       *llminteraction.QuestionAnswer
	answerCalled  bool
	clarifyCalled bool
}

func (s *stubConversationLLM) DecideFinalAction(ctx context.Context, text string, candidates []llminteraction.ActionCandidate) (*llminteraction.ActionDecision, error) {
	return nil, nil
}

func (s *stubConversationLLM) AnswerQuestion(ctx context.Context, text string) (*llminteraction.QuestionAnswer, error) {
	s.answerCalled = true
	return s.answer, nil
}

func (s *stubConversationLLM) AnalyzeContext(ctx context.Context, prompt string, text string) (*llminteraction.ContextAnalysis, error) {
	// conversationflow 測試不會進入 realtime implicit reply linker；
	// 這裡只補齊 InteractionService 介面，避免 unrelated test 因新能力擴充而失焦。
	return nil, nil
}

func (s *stubConversationLLM) AnalyzeTodo(ctx context.Context, prompt string, text string) (*llminteraction.TodoAnalysis, error) {
	// conversationflow 測試不會進入 realtime Todo structured analyzer；這裡只補齊介面。
	return nil, nil
}

func (s *stubConversationLLM) AnalyzeTodoDueTime(ctx context.Context, prompt string, text string) (*llminteraction.TodoDueTimeAnalysis, error) {
	// conversationflow 測試不會進入 realtime Todo due-time normalizer；這裡只補齊介面。
	return nil, nil
}

func (s *stubConversationLLM) AskClarifyingQuestion(ctx context.Context, text string, reason string) (*llminteraction.QuestionAnswer, error) {
	s.clarifyCalled = true
	return s.clarify, nil
}

type stubOutboundMessenger struct {
	sendCalled bool
	chatID     string
	userID     string
	text       string
	replyRef   string
	quoteRef   string
}

func (s *stubOutboundMessenger) SendText(ctx context.Context, chatID string, userID string, text string, replyRef string, quoteRef string) (string, error) {
	s.sendCalled = true
	s.chatID = chatID
	s.userID = userID
	s.text = text
	s.replyRef = replyRef
	s.quoteRef = quoteRef
	return "sent-123", nil
}

// TestBuildFixedChainOperationCandidatesCarriesPrompt 驗證指令鍊固定候選會帶入 seed 動態 prompt。
func TestBuildFixedChainOperationCandidatesCarriesPrompt(t *testing.T) {
	o := &Orchestrator{}
	candidates := o.buildFixedChainOperationCandidates(&chainCommandContext{
		APIOperation: "start_translation_locale",
		SkillCode:    "channel.translation",
		ActionPrompt: "規則: target_locales 請輸出 xx-YY。",
	})

	if len(candidates) != 1 {
		t.Fatalf("expected one candidate, got %d", len(candidates))
	}
	if candidates[0].Operation != "start_translation_locale" {
		t.Fatalf("unexpected operation: %s", candidates[0].Operation)
	}
	if candidates[0].Prompt == "" {
		t.Fatal("expected candidate prompt to be carried from chain context")
	}
}

func TestBuildCommandDecisionInputTextCarriesChainContext(t *testing.T) {
	o := &Orchestrator{}
	text := o.buildCommandDecisionInputText("德文法文跟西班牙文", &chainCommandContext{
		APIOperation:      "start_translation_locale",
		MissingParameters: []string{"target_locales"},
	})

	if !strings.Contains(text, "mode=chain_parameter_fill") {
		t.Fatalf("expected chain phase marker, got: %s", text)
	}

	if !strings.Contains(text, "api_operation=start_translation_locale") {
		t.Fatalf("expected chain operation in context, got: %s", text)
	}
	if !strings.Contains(text, "missing_parameters=target_locales") {
		t.Fatalf("expected missing parameters in context, got: %s", text)
	}
}

func TestBuildCommandDecisionInputTextSkipsExtraPolicyText(t *testing.T) {
	o := &Orchestrator{}
	text := o.buildCommandDecisionInputText("收到", &chainCommandContext{
		APIOperation:      "start_translation_locale",
		MissingParameters: []string{"something_else"},
	})

	if strings.Contains(text, "[parameter_fill_policy]") {
		t.Fatalf("did not expect hardcoded policy block: %s", text)
	}
	if strings.Contains(text, "德文=de-DE") || strings.Contains(text, "英文=en-US") {
		t.Fatalf("did not expect locale mapping examples in orchestrator prompt: %s", text)
	}
}

func TestBuildMissingParameterTemplateQuestionIsGeneric(t *testing.T) {
	got := buildMissingParameterTemplateQuestion([]string{"target_locales"})
	if !strings.Contains(got, "請補充以下必要資訊後我才能執行指令：") {
		t.Fatalf("expected generic template, got: %s", got)
	}
	if strings.Contains(got, "英文") || strings.Contains(got, "日文") || strings.Contains(got, "翻譯成哪些語言") {
		t.Fatalf("did not expect translation-specific wording, got: %s", got)
	}
}

func TestBuildInitialDecisionInputTextAddsPhaseMarker(t *testing.T) {
	text := buildInitialDecisionInputText("@Jarvis 開啟翻譯")
	if !strings.Contains(text, "mode=initial_action_decision") {
		t.Fatalf("expected initial decision phase marker, got: %s", text)
	}
	if !strings.Contains(text, "[user_message]") {
		t.Fatalf("expected user_message section, got: %s", text)
	}
}

func TestInferClarifyingOperation(t *testing.T) {
	tests := []struct {
		name       string
		current    string
		candidates []llminteraction.ActionCandidate
		chainCtx   *chainCommandContext
		want       string
	}{
		{name: "prefer current decision operation", current: "start_translation_locale", want: "start_translation_locale"},
		{name: "fallback to chain operation", current: "", chainCtx: &chainCommandContext{APIOperation: "stop_translation_locale"}, want: "stop_translation_locale"},
		{name: "do not infer from unrelated candidates", current: "", candidates: []llminteraction.ActionCandidate{{Operation: "start_translation_locale"}}, want: ""},
		{name: "empty when no source available", current: "", candidates: nil, chainCtx: nil, want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := inferClarifyingOperation(tt.current, tt.candidates, tt.chainCtx)
			if got != tt.want {
				t.Fatalf("unexpected inferred operation: got=%q want=%q", got, tt.want)
			}
		})
	}
}

func TestRouteMessageToQuestionAnswerSendsAnswerToUser(t *testing.T) {
	// 驗證 shared conversationflow 的一般問答結果真的會發送給使用者，
	// 而不是只停在內部 log 或模型回傳物件。
	llmStub := &stubConversationLLM{
		answer: &llminteraction.QuestionAnswer{SchemaVersion: "v1", Answer: "你可以看《F1電影》。", Confidence: 0.82},
	}
	messengerStub := &stubOutboundMessenger{}
	o := New(Dependencies{
		PlatformLabel: "slack",
		LLM:           llmStub,
		Messenger:     messengerStub,
	})

	o.routeMessageToQuestionAnswer(context.Background(), &unifiedmessage.Message{
		ChannelID:         "D123",
		PlatformMessageID: "1784306143.602749",
		Text:              "推薦幾部現在最新的電影吧",
	}, nil, "U123", 0, 0.7, "answer_question", "一般問答", nil)

	if !llmStub.answerCalled {
		t.Fatal("expected AnswerQuestion to be called")
	}
	if llmStub.clarifyCalled {
		t.Fatal("did not expect AskClarifyingQuestion to be called")
	}
	if !messengerStub.sendCalled {
		t.Fatal("expected answer to be sent to user")
	}
	if messengerStub.chatID != "D123" || messengerStub.userID != "U123" {
		t.Fatalf("unexpected send target: chatID=%q userID=%q", messengerStub.chatID, messengerStub.userID)
	}
	if messengerStub.text != "你可以看《F1電影》。" {
		t.Fatalf("unexpected outbound text: %q", messengerStub.text)
	}
	if messengerStub.replyRef != "" || messengerStub.quoteRef != "" {
		t.Fatalf("expected non-reply answer send, got replyRef=%q quoteRef=%q", messengerStub.replyRef, messengerStub.quoteRef)
	}
}
