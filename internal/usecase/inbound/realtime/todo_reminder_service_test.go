package realtime

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"assistant-api/internal/ent"
	"assistant-api/internal/integration/unifiedmessage"
	llminteraction "assistant-api/internal/usecase/ai/llm_interaction"
	"assistant-api/internal/usecase/ai/reranker"

	"github.com/google/uuid"
)

type stubRecentMessageStore struct {
	calls             int
	parentCalls       int
	windowCalls       int
	candidateCalls    int
	parent            *ent.ChannelMessage
	parentByMessageID map[uuid.UUID]*ent.ChannelMessage
	windowsByAnchorID map[uuid.UUID][]*ent.ChannelMessage
	items             []*ent.ChannelMessage
	candidates        []*ent.TodoCandidate
}

func (s *stubRecentMessageStore) ResolveParentMessage(ctx context.Context, message *ent.ChannelMessage) (*ent.ChannelMessage, error) {
	_ = ctx
	s.parentCalls++
	if s.parentByMessageID != nil && message != nil {
		return s.parentByMessageID[message.ID], nil
	}
	return s.parent, nil
}

func (s *stubRecentMessageStore) FindMessageWindowAround(ctx context.Context, message *ent.ChannelMessage, beforeLimit int, afterLimit int) ([]*ent.ChannelMessage, error) {
	_ = ctx
	_ = beforeLimit
	_ = afterLimit
	s.windowCalls++
	if s.windowsByAnchorID != nil && message != nil {
		return s.windowsByAnchorID[message.ID], nil
	}
	if message != nil {
		return []*ent.ChannelMessage{message}, nil
	}
	return nil, nil
}

func (s *stubRecentMessageStore) FindRecentMessagesBefore(ctx context.Context, message *ent.ChannelMessage, limit int) ([]*ent.ChannelMessage, error) {
	// 這個 stub 只驗證 usecase 是否真的向 repository 要近端歷史訊息；
	// message/limit 的細節已由呼叫端組裝，因此此處記錄呼叫次數即可避免測試過度耦合查詢實作。
	_ = ctx
	_ = message
	_ = limit
	s.calls++
	return s.items, nil
}

func (s *stubRecentMessageStore) FindTodoCandidatesByMessageIDs(ctx context.Context, channelID uuid.UUID, messageIDs []uuid.UUID) ([]*ent.TodoCandidate, error) {
	_ = ctx
	_ = channelID
	_ = messageIDs
	s.candidateCalls++
	return s.candidates, nil
}

type stubContextAnalyzer struct {
	calls         int
	prompt        string
	text          string
	dueTimePrompt string
	dueTimeText   string
	todoResult    *llminteraction.TodoAnalysis
	dueTimeResult *llminteraction.TodoDueTimeAnalysis
}

type blockingTodoAnalyzer struct {
	mu          sync.Mutex
	calls       int
	started     chan string
	release     chan struct{}
	secondBegin chan struct{}
}

type stubImplicitReplyRanker struct {
	calls     int
	documents []string
}

func (s *stubImplicitReplyRanker) Rerank(ctx context.Context, query string, documents []string, topK int) ([]reranker.RankedDocument, error) {
	// 固定把第二筆排到第一筆前面，用來驗證 todo reminder 會尊重 reranker 回傳順序，
	// 而不是永遠使用 repository 的時間序。這能防止未來重構時不小心把精排結果丟掉。
	_ = ctx
	_ = query
	_ = topK
	s.calls++
	s.documents = append([]string(nil), documents...)
	return []reranker.RankedDocument{
		{Index: 1, Document: documents[1], Score: 0.91},
		{Index: 0, Document: documents[0], Score: 0.54},
	}, nil
}

func (s *stubContextAnalyzer) DecideFinalAction(ctx context.Context, text string, candidates []llminteraction.ActionCandidate) (*llminteraction.ActionDecision, error) {
	return nil, nil
}

func (s *stubContextAnalyzer) AnswerQuestion(ctx context.Context, text string) (*llminteraction.QuestionAnswer, error) {
	return nil, nil
}

func (s *stubContextAnalyzer) AnalyzeContext(ctx context.Context, prompt string, text string) (*llminteraction.ContextAnalysis, error) {
	return nil, nil
}

func (s *stubContextAnalyzer) AnalyzeTodo(ctx context.Context, prompt string, text string) (*llminteraction.TodoAnalysis, error) {
	// todo analyzer stub 會保存 prompt/text，讓測試可以直接檢查：
	// 1. prompt 是否包含近端候選訊息。
	// 2. text 是否仍是目前使用者輸入，而不是被替換成歷史訊息。
	_ = ctx
	s.calls++
	s.prompt = prompt
	s.text = text
	if s.todoResult != nil {
		return s.todoResult, nil
	}
	return &llminteraction.TodoAnalysis{SchemaVersion: "v1", Decision: "update_candidate", LinkedMessageID: "recent-message", Summary: "補報價單", Confidence: 0.82, Reason: "接續前文待辦"}, nil
}

func (s *stubContextAnalyzer) AnalyzeTodoDueTime(ctx context.Context, prompt string, text string) (*llminteraction.TodoDueTimeAnalysis, error) {
	_ = ctx
	s.dueTimePrompt = prompt
	s.dueTimeText = text
	if s.dueTimeResult != nil {
		return s.dueTimeResult, nil
	}
	return &llminteraction.TodoDueTimeAnalysis{SchemaVersion: "v1", Decision: "no_due_time", Precision: "unknown", Confidence: 0.5, Reason: "test stub"}, nil
}

func (s *blockingTodoAnalyzer) DecideFinalAction(ctx context.Context, text string, candidates []llminteraction.ActionCandidate) (*llminteraction.ActionDecision, error) {
	return nil, nil
}

func (s *blockingTodoAnalyzer) AnswerQuestion(ctx context.Context, text string) (*llminteraction.QuestionAnswer, error) {
	return nil, nil
}

func (s *blockingTodoAnalyzer) AnalyzeContext(ctx context.Context, prompt string, text string) (*llminteraction.ContextAnalysis, error) {
	return nil, nil
}

func (s *blockingTodoAnalyzer) AskClarifyingQuestion(ctx context.Context, text string, reason string) (*llminteraction.QuestionAnswer, error) {
	return nil, nil
}

func (s *blockingTodoAnalyzer) AnalyzeTodo(ctx context.Context, prompt string, text string) (*llminteraction.TodoAnalysis, error) {
	s.mu.Lock()
	s.calls++
	callNumber := s.calls
	s.mu.Unlock()
	if s.started != nil {
		s.started <- text
	}
	if callNumber == 1 && s.release != nil {
		<-s.release
	}
	if callNumber == 2 && s.secondBegin != nil {
		close(s.secondBegin)
	}
	return &llminteraction.TodoAnalysis{SchemaVersion: "v1", Decision: "no_action", Confidence: 0.2, Reason: "test"}, nil
}

func (s *blockingTodoAnalyzer) AnalyzeTodoDueTime(ctx context.Context, prompt string, text string) (*llminteraction.TodoDueTimeAnalysis, error) {
	return &llminteraction.TodoDueTimeAnalysis{SchemaVersion: "v1", Decision: "no_due_time", Precision: "unknown", Confidence: 0.2, Reason: "test"}, nil
}

func (s *stubContextAnalyzer) AskClarifyingQuestion(ctx context.Context, text string, reason string) (*llminteraction.QuestionAnswer, error) {
	return nil, nil
}

func TestTodoReminderServiceAnalyzesImplicitReplyContext(t *testing.T) {
	// 驗證核心中期流程：classifier 打出 candidate 後，todo reminder 會取最近訊息，
	// 再把目前短句與候選上下文交給 todo analyzer 做 structured decision。
	channelID := uuid.New()
	currentMessageID := uuid.New()
	recentMessageID := uuid.New()
	candidateID := uuid.New()
	repo := &stubRecentMessageStore{items: []*ent.ChannelMessage{
		{ID: recentMessageID, ChannelID: channelID, SenderName: "阿明", Content: "那報價單今天誰處理一下", CreatedAt: time.Now().Add(-time.Minute)},
	}, candidates: []*ent.TodoCandidate{
		{ID: candidateID, ChannelID: channelID, SourceMessageID: recentMessageID, LastMessageID: recentMessageID, Status: "needs_more_info", LastDecision: "needs_more_info", Summary: "模型舊摘要不應污染 prompt", MissingFields: []string{"due_text"}, Confidence: 0.62, Reason: "模型舊原因不應污染 prompt"},
	}}
	analyzer := &stubContextAnalyzer{}
	service := NewTodoReminderService(TodoReminderServiceOptions{Repo: repo, LLM: analyzer, PlatformLabel: "test", RecentLimit: 4, ReplyChainMaxDepth: 4})

	service.HandleClassification(context.Background(), MessageContext{
		Message:      &unifiedmessage.Message{ChannelID: "channel-1", PlatformMessageID: "m-2", MessageType: "text", Text: "我晚點弄"},
		SavedMessage: &ent.ChannelMessage{ID: currentMessageID, ChannelID: channelID, Content: "我晚點弄", CreatedAt: time.Now()},
	}, ClassificationResult{Tag: "todo", Signal: ClassificationSignalCandidate, Confidence: 0.9})

	if repo.calls != 1 {
		t.Fatalf("expected recent message store to be called once, got %d", repo.calls)
	}
	if repo.candidateCalls != 1 {
		t.Fatalf("expected todo candidate context store to be called once, got %d", repo.candidateCalls)
	}
	if analyzer.calls != 1 {
		t.Fatalf("expected todo analyzer to be called once, got %d", analyzer.calls)
	}
	if analyzer.text != "我晚點弄" {
		t.Fatalf("expected analyzer text to use current message, got %q", analyzer.text)
	}
	if !strings.Contains(analyzer.prompt, recentMessageID.String()) || !strings.Contains(analyzer.prompt, "那報價單今天誰處理一下") {
		t.Fatalf("expected prompt to include recent candidate context, got %q", analyzer.prompt)
	}
	for _, expected := range []string{"Todo candidate contexts", recentMessageID.String(), "status=needs_more_info", "last_decision=needs_more_info", "missing_fields=[due_text]"} {
		if !strings.Contains(analyzer.prompt, expected) {
			t.Fatalf("expected prompt to include structured todo candidate context %q, got %q", expected, analyzer.prompt)
		}
	}
	candidateContextSection := analyzer.prompt
	if marker := strings.Index(analyzer.prompt, "Todo candidate contexts:"); marker >= 0 {
		candidateContextSection = analyzer.prompt[marker:]
	}
	for _, unexpected := range []string{candidateID.String(), "candidate_id=", "模型舊摘要不應污染 prompt", "模型舊原因不應污染 prompt", "summary=", "reason="} {
		if strings.Contains(candidateContextSection, unexpected) {
			t.Fatalf("expected todo candidate context section to omit prior AI-generated candidate field %q, got %q", unexpected, candidateContextSection)
		}
	}
	if !strings.Contains(analyzer.prompt, "todo_analysis JSON contract") {
		t.Fatalf("expected prompt to use todo analysis contract, got %q", analyzer.prompt)
	}
}

func TestTodoReminderServiceSkipsUnclearNonTodoWithoutCandidateContext(t *testing.T) {
	// classifier 認為不是 todo top label 的 unclear 訊息，若近端也沒有待辦候選狀態，
	// 只保留在訊息歷史中作為後續 context，不送進 todo analyzer 浪費 LLM。
	channelID := uuid.New()
	recentMessageID := uuid.New()
	repo := &stubRecentMessageStore{items: []*ent.ChannelMessage{
		{ID: recentMessageID, ChannelID: channelID, SenderName: "葛育平", Content: "昨天伺服器運作好像不太穩定", CreatedAt: time.Now().Add(-time.Minute)},
	}}
	analyzer := &stubContextAnalyzer{}
	service := NewTodoReminderService(TodoReminderServiceOptions{Repo: repo, LLM: analyzer, PlatformLabel: "test", RecentLimit: 4, ReplyChainMaxDepth: 4})

	service.HandleClassification(context.Background(), MessageContext{
		Message:      &unifiedmessage.Message{ChannelID: "channel-1", PlatformMessageID: "m-2", MessageType: "text", Text: "昨天伺服器運作好像不太穩定"},
		SavedMessage: &ent.ChannelMessage{ID: uuid.New(), ChannelID: channelID, Content: "昨天伺服器運作好像不太穩定", CreatedAt: time.Now()},
	}, ClassificationResult{Tag: "question", Signal: ClassificationSignalUnclear, Confidence: 0.53, ScoreMargin: 0.07})

	if repo.calls != 1 {
		t.Fatalf("expected recent message store to be called once for state-aware gate, got %d", repo.calls)
	}
	if repo.candidateCalls != 1 {
		t.Fatalf("expected candidate context store to be called once for state-aware gate, got %d", repo.candidateCalls)
	}
	if analyzer.calls != 0 {
		t.Fatalf("expected todo analyzer to be skipped for unclear non-todo without candidate context, got %d calls", analyzer.calls)
	}
}

func TestTodoReminderServiceAnalyzesUnclearNonTodoWithCandidateContext(t *testing.T) {
	// unclear 且 top label 不是 todo 時不能一律跳過；若近端已有待辦候選狀態，
	// 短回覆仍可能是在承接該狀態，應交給 structured analyzer 做語用判斷。
	channelID := uuid.New()
	recentMessageID := uuid.New()
	repo := &stubRecentMessageStore{items: []*ent.ChannelMessage{
		{ID: recentMessageID, ChannelID: channelID, SenderName: "奧斯卡", Content: "那你明天把原因統整好之後告訴我", CreatedAt: time.Now().Add(-time.Minute)},
	}, candidates: []*ent.TodoCandidate{
		{ID: uuid.New(), ChannelID: channelID, SourceMessageID: recentMessageID, LastMessageID: recentMessageID, Status: "pending_update", LastDecision: "update_candidate", MissingFields: []string{}},
	}}
	analyzer := &stubContextAnalyzer{}
	service := NewTodoReminderService(TodoReminderServiceOptions{Repo: repo, LLM: analyzer, PlatformLabel: "test", RecentLimit: 4, ReplyChainMaxDepth: 4})

	service.HandleClassification(context.Background(), MessageContext{
		Message:      &unifiedmessage.Message{ChannelID: "channel-1", PlatformMessageID: "m-2", MessageType: "text", Text: "行"},
		SavedMessage: &ent.ChannelMessage{ID: uuid.New(), ChannelID: channelID, Content: "行", CreatedAt: time.Now()},
	}, ClassificationResult{Tag: "question", Signal: ClassificationSignalUnclear, Confidence: 0.53, ScoreMargin: 0.07})

	if repo.candidateCalls != 1 {
		t.Fatalf("expected candidate context store to be called once, got %d", repo.candidateCalls)
	}
	if analyzer.calls != 1 {
		t.Fatalf("expected todo analyzer to run when unclear non-todo has candidate context, got %d calls", analyzer.calls)
	}
}

func TestTodoReminderServiceSerializesAnalysisPerChannel(t *testing.T) {
	// Todo analyzer 依賴前一則訊息完成 candidate persistence 後的狀態；
	// 同 channel 的下一則若並行先跑，短確認句會看不到剛建立的 update proposal。
	channelID := uuid.New()
	repo := &stubRecentMessageStore{items: []*ent.ChannelMessage{
		{ID: uuid.New(), ChannelID: channelID, SenderName: "奧斯卡", Content: "那你明天把原因統整好之後告訴我", CreatedAt: time.Now().Add(-time.Minute)},
	}}
	analyzer := &blockingTodoAnalyzer{started: make(chan string, 2), release: make(chan struct{}), secondBegin: make(chan struct{})}
	service := NewTodoReminderService(TodoReminderServiceOptions{Repo: repo, LLM: analyzer, PlatformLabel: "test", RecentLimit: 4, ReplyChainMaxDepth: 4})
	result := ClassificationResult{Tag: "todo", Signal: ClassificationSignalCandidate, Confidence: 0.9}

	go service.HandleClassification(context.Background(), MessageContext{
		Message:      &unifiedmessage.Message{ChannelID: "line-group-1", PlatformMessageID: "m-1", MessageType: "text", Text: "我明天請假, 後天早上行嗎?"},
		SavedMessage: &ent.ChannelMessage{ID: uuid.New(), ChannelID: channelID, Content: "我明天請假, 後天早上行嗎?", CreatedAt: time.Now()},
	}, result)

	if got := <-analyzer.started; got != "我明天請假, 後天早上行嗎?" {
		t.Fatalf("expected first analyzer call to start with first message, got %q", got)
	}

	go service.HandleClassification(context.Background(), MessageContext{
		Message:      &unifiedmessage.Message{ChannelID: "line-group-1", PlatformMessageID: "m-2", MessageType: "text", Text: "行"},
		SavedMessage: &ent.ChannelMessage{ID: uuid.New(), ChannelID: channelID, Content: "行", CreatedAt: time.Now()},
	}, result)

	select {
	case <-analyzer.secondBegin:
		t.Fatal("expected second same-channel analyzer call to wait for first call")
	case <-time.After(20 * time.Millisecond):
	}
	close(analyzer.release)
	select {
	case got := <-analyzer.started:
		if got != "行" {
			t.Fatalf("expected second analyzer call to process acknowledgement, got %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("expected second analyzer call to start after first call released")
	}
}

func TestBuildImplicitReplyTodoPromptRequiresArrayFields(t *testing.T) {
	// 本地小模型容易把 assignees 輸出成字串或物件；prompt 必須明確鎖住 array<string>，
	// 讓 Python validator 的 validation retry 可以修正同一份 contract，而不是放寬解析規則。
	prompt := buildImplicitReplyTodoPrompt(nil, nil, nil, nil, ClassificationResult{Tag: "todo", Signal: ClassificationSignalCandidate, Confidence: 0.9})

	if !strings.Contains(prompt, "assignees 與 missing_fields 永遠是 string array") {
		t.Fatalf("expected prompt to require string array fields, got %q", prompt)
	}
	if !strings.Contains(prompt, `"assignees":[]`) || !strings.Contains(prompt, `"missing_fields":[]`) {
		t.Fatalf("expected prompt JSON shape to show empty arrays, got %q", prompt)
	}
}

func TestBuildImplicitReplyTodoPromptRejectsFragmentKeys(t *testing.T) {
	// qwen 2b 類小模型偶爾會把 JSON 片段拼進欄位名稱，例如 due_text\":\"\"；
	// 首輪 prompt 必須先禁止這種輸出，避免都等到 Python json retry 才修正。
	prompt := buildImplicitReplyTodoPrompt(nil, nil, nil, nil, ClassificationResult{Tag: "todo", Signal: ClassificationSignalCandidate, Confidence: 0.9})

	for _, expected := range []string{
		"JSON key 必須只使用",
		"不可把 JSON 片段、跳脫字元或 key/value 片段放進欄位名稱",
		`due_text\":\"\"`,
	} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("expected prompt to reject malformed fragment key %q, got %q", expected, prompt)
		}
	}
}

func TestBuildImplicitReplyTodoPromptTreatsReminderLanguageAsCandidate(t *testing.T) {
	// 使用者常用「提醒我」「記得」這類日常語氣建立待辦；prompt 必須明確要求 analyzer
	// 依可追蹤事項判斷，而不是因為語氣不像正式指令就回 no_action。
	prompt := buildImplicitReplyTodoPrompt(nil, nil, nil, nil, ClassificationResult{Tag: "todo", Signal: ClassificationSignalCandidate, Confidence: 0.9})

	if !strings.Contains(prompt, "日常提醒語氣") || !strings.Contains(prompt, "不可只因為語氣日常就判 no_action") {
		t.Fatalf("expected prompt to treat reminder language as todo candidate, got %q", prompt)
	}
}

func TestBuildImplicitReplyTodoPromptUsesChronologicalOrderForReplySemantics(t *testing.T) {
	// reranker 排序適合找語意候選，但短確認/改期要看實際對話輪次；
	// 否則模型會被舊 candidate context 拉走，忽略最近一則提案或改期詢問。
	prompt := buildImplicitReplyTodoPrompt(nil, nil, nil, nil, ClassificationResult{Tag: "todo", Signal: ClassificationSignalCandidate, Confidence: 0.9})

	for _, expected := range []string{
		"Conversation messages in chronological order 是實際對話輪次",
		"用 chronological order 決定最近被回覆的提案/詢問",
		"不可只看 rerank 排名",
		"Mandatory decision procedure",
		"先判 Current message 的語用類型，再判欄位完整性",
		"Current message 本身包含可追蹤行動、交付內容、時間安排、承諾或請求他人完成事項",
		"這是任務內容訊息，優先輸出 create_candidate 或 update_candidate",
		"不可因 Latest prior conversation message 是問題/狀態回覆而輸出 acknowledge",
		"個人可用性、請假、行程或狀態描述只有在 standalone 且沒有承接既有任務時才可判 no_action",
		"若它是在回覆前文交辦、提案或待確認任務",
		"必須依同一任務輸出 update_candidate，而不是 no_action",
		"提出替代時間/替代條件/詢問替代安排是否可行",
		"輸出 update_candidate",
		"只有 Current message 是短確認、短否定或短可行性答覆",
		"且沒有新的任務內容、替代時間或替代條件時，才使用 acknowledge",
		"上一則相鄰訊息本身是提案、詢問或改期請求",
		"即使該上一則尚未出現在 Todo candidate contexts",
		"不要為了使用既有 candidate context 而連到更舊、語用功能不同的訊息",
	} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("expected prompt to contain chronological reply rule %q, got %q", expected, prompt)
		}
	}
}

func TestBuildImplicitReplyTodoPromptMarksLatestPriorMessageAsAdjacencyTarget(t *testing.T) {
	// 對「行」這類短回覆，小模型容易只看 Current message 本身而回 no_action；
	// prompt 必須把時間序最後一則歷史訊息明確標成相鄰 target candidate。
	channelID := uuid.New()
	olderMessageID := uuid.New()
	latestMessageID := uuid.New()
	conversationMessages := []*ent.ChannelMessage{
		{ID: olderMessageID, ChannelID: channelID, SenderName: "奧斯卡", Content: "那你明天把原因統整好之後告訴我", CreatedAt: time.Now().Add(-2 * time.Minute)},
		{ID: latestMessageID, ChannelID: channelID, SenderName: "葛育平", Content: "我明天請假, 後天早上行嗎?", CreatedAt: time.Now().Add(-time.Minute)},
	}
	prompt := buildImplicitReplyTodoPrompt(nil, conversationMessages, conversationMessages, nil, ClassificationResult{Tag: "todo", Signal: ClassificationSignalCandidate, Confidence: 0.9})

	latestSectionIndex := strings.Index(prompt, "Latest prior conversation message:")
	if latestSectionIndex < 0 {
		t.Fatalf("expected prompt to include latest prior message section, got %q", prompt)
	}
	latestSection := prompt[latestSectionIndex:]
	for _, expected := range []string{
		latestMessageID.String(),
		`text="我明天請假, 後天早上行嗎?"`,
		"Use this as the first adjacency target candidate",
		"dialogue target selection",
		"Latest prior conversation message 是 Current message 的相鄰回覆目標候選",
		"decision 應為 acknowledge",
		"linked_message_id 指向 Latest prior conversation message 的 id",
		"不能覆蓋相鄰對話目標",
		"才可以因 Current message 本身缺少任務目標或欄位而輸出 no_action",
		"decision precedence",
		"Current message 本身的新任務內容或替代安排",
		"Latest prior conversation message 的短確認/短否定/可行性答覆",
		"若前面任一層成立，不可再因 Current message 本身很短或缺欄位而改成 no_action",
		"Current message 本身包含任務內容/交付/時間安排時的合法 JSON 範例優先於 adjacency acknowledge",
		`"decision":"create_candidate"`,
		"Current message 本身提出可追蹤交付內容與時間安排",
		"個人可用性或行程衝突承接既有任務並提出替代安排時的合法 JSON 範例優先於 no_action",
		`"decision":"update_candidate"`,
		`"linked_message_id":"<message_id_from_context>"`,
		"Current message 是對既有任務的可用性/替代安排回覆",
		"短確認承接 Latest prior conversation message 的合法 JSON 範例優先於 no_action 範例",
		`"decision":"acknowledge"`,
		`"linked_message_id":"<latest_prior_message_id>"`,
		"Current message 是對 Latest prior conversation message 的短確認或可行性答覆",
	} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("expected prompt to contain adjacency target guidance %q, got %q", expected, prompt)
		}
	}
	if strings.Contains(latestSection, olderMessageID.String()) {
		t.Fatalf("expected latest prior section to contain only the latest message, got %q", latestSection)
	}
}

func TestBuildTodoDueTimePromptRejectsNullAndRequiresUnknownPrecision(t *testing.T) {
	// due-time normalizer 的 needs_more_info/no_due_time 也必須符合固定 schema；
	// 小模型不可用 null 或省略 precision，否則 9003 strict validator 會拒絕。
	prompt := buildTodoDueTimePrompt(
		MessageContext{Message: &unifiedmessage.Message{Text: "提醒我晚點處理"}},
		&llminteraction.TodoAnalysis{Summary: "處理事項", DueText: "晚點"},
		time.Date(2026, 7, 21, 10, 30, 0, 0, time.FixedZone("Asia/Taipei", 8*60*60)),
		"Asia/Taipei",
	)

	for _, expected := range []string{
		"不可輸出 null",
		"reason 是必填非空字串",
		"precision 使用 unknown",
		`"precision":"unknown"`,
		`"missing_fields":["time"]`,
	} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("expected due-time prompt to contain %q, got %q", expected, prompt)
		}
	}
}

func TestBuildTodoDueTimePromptRequiresRelativeDatesFromReferenceTime(t *testing.T) {
	// 「明天」這類相對日期必須以訊息建立時間換算，不能用測試/伺服器執行當下日期，
	// 否則 replay 舊訊息或跨時區部署時會把待辦排到錯誤日期。
	referenceTime := time.Date(2026, 7, 21, 17, 55, 0, 0, time.FixedZone("Asia/Taipei", 8*60*60))
	prompt := buildTodoDueTimePrompt(
		MessageContext{Message: &unifiedmessage.Message{Text: "那你明天把原因統整好之後告訴我"}},
		&llminteraction.TodoAnalysis{Summary: "統整伺服器 CPU 滿載原因並回報", DueText: "明天"},
		referenceTime,
		"Asia/Taipei",
	)

	for _, expected := range []string{
		"相對日期必須以 reference_time",
		"明天 代表 reference_time 日期加一天",
		"不可使用模型執行當下日期或伺服器現在時間",
		"reference_time=2026-07-21T17:55:00+08:00",
		`due_text="明天"`,
	} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("expected relative-date prompt to contain %q, got %q", expected, prompt)
		}
	}
}

func TestTodoReminderServicePersistsTodoCandidateAnalysis(t *testing.T) {
	// structured analyzer 已經完成語意判斷後，realtime service 只把固定 schema 轉成 candidate persistence input；
	// 這裡鎖住 create_candidate 會用目前訊息當 source/last message，不再停留在純 log-only。
	channelID := uuid.New()
	currentMessageID := uuid.New()
	recentMessageID := uuid.New()
	repo := &stubRecentMessageStore{items: []*ent.ChannelMessage{
		{ID: recentMessageID, ChannelID: channelID, SenderName: "阿明", Content: "明天記得交報價單", CreatedAt: time.Now().Add(-time.Minute)},
	}}
	analyzer := &stubContextAnalyzer{todoResult: &llminteraction.TodoAnalysis{
		SchemaVersion: "v1",
		Decision:      "create_candidate",
		Summary:       "明天交報價單",
		Assignees:     []string{"我"},
		DueText:       "明天",
		Confidence:    0.91,
		Reason:        "message describes a todo",
	}}
	var persisted TodoCandidateInput
	persistCalls := 0
	service := NewTodoReminderService(TodoReminderServiceOptions{
		Repo:          repo,
		LLM:           analyzer,
		PlatformLabel: "test",
		RecentLimit:   4,
		PersistTodoCandidate: func(ctx context.Context, input TodoCandidateInput) (*ent.TodoCandidate, error) {
			_ = ctx
			persistCalls++
			persisted = input
			return &ent.TodoCandidate{ID: uuid.New()}, nil
		},
	})

	service.HandleClassification(context.Background(), MessageContext{
		Message:      &unifiedmessage.Message{ChannelID: "channel-1", PlatformMessageID: "m-2", MessageType: "text", Text: "明天我來交報價單"},
		SavedMessage: &ent.ChannelMessage{ID: currentMessageID, ChannelID: channelID, Content: "明天我來交報價單", CreatedAt: time.Now()},
	}, ClassificationResult{Tag: "todo", Signal: ClassificationSignalCandidate, Confidence: 0.9})

	if persistCalls != 1 {
		t.Fatalf("expected candidate persistence to be called once, got %d", persistCalls)
	}
	if persisted.ChannelID != channelID || persisted.MessageID != currentMessageID {
		t.Fatalf("unexpected persistence message identity: %+v", persisted)
	}
	if persisted.Decision != "create_candidate" || persisted.Summary != "明天交報價單" || persisted.DueText != "明天" {
		t.Fatalf("unexpected persistence payload: %+v", persisted)
	}
	if len(persisted.Assignees) != 1 || persisted.Assignees[0] != "我" {
		t.Fatalf("unexpected assignees: %+v", persisted.Assignees)
	}
}

func TestTodoReminderServicePersistsTomorrowDueAtFromMessageDate(t *testing.T) {
	// 使用者在 2026-07-21 17:55 說「明天」時，due normalizer 必須拿該訊息時間作 reference_time，
	// 並把 normalized due_at 傳進 candidate persistence，而不是只保存 due_text 字面。
	channelID := uuid.New()
	currentMessageID := uuid.New()
	recentMessageID := uuid.New()
	location := time.FixedZone("Asia/Taipei", 8*60*60)
	messageTime := time.Date(2026, 7, 21, 17, 55, 0, 0, location)
	expectedDueAt := time.Date(2026, 7, 22, 9, 0, 0, 0, location)
	repo := &stubRecentMessageStore{items: []*ent.ChannelMessage{
		{ID: recentMessageID, ChannelID: channelID, SenderName: "葛育平(Oscar)", Content: "半夜 3 點左右，整個伺服器 CPU 運作 100%", CreatedAt: messageTime.Add(-time.Minute)},
	}}
	analyzer := &stubContextAnalyzer{
		todoResult: &llminteraction.TodoAnalysis{
			SchemaVersion: "v1",
			Decision:      "create_candidate",
			Summary:       "統整伺服器 CPU 滿載原因並回報",
			Assignees:     []string{"葛育平(Oscar)"},
			DueText:       "明天",
			Confidence:    0.92,
			Reason:        "user assigns a follow-up investigation",
		},
		dueTimeResult: &llminteraction.TodoDueTimeAnalysis{
			SchemaVersion: "v1",
			Decision:      "normalized",
			DueAt:         "2026-07-22T09:00:00+08:00",
			Timezone:      "Asia/Taipei",
			Precision:     "date",
			Confidence:    0.86,
			Reason:        "明天以 reference_time 日期加一天換算，未指定時間所以使用日期候選提醒時間",
		},
	}
	var persisted TodoCandidateInput
	service := NewTodoReminderService(TodoReminderServiceOptions{
		Repo:          repo,
		LLM:           analyzer,
		PlatformLabel: "test",
		RecentLimit:   4,
		Timezone:      "Asia/Taipei",
		PersistTodoCandidate: func(ctx context.Context, input TodoCandidateInput) (*ent.TodoCandidate, error) {
			_ = ctx
			persisted = input
			return &ent.TodoCandidate{ID: uuid.New()}, nil
		},
	})

	service.HandleClassification(context.Background(), MessageContext{
		Message:      &unifiedmessage.Message{ChannelID: "channel-1", PlatformMessageID: "m-6", MessageType: "text", Text: "那你明天把原因統整好之後告訴我"},
		SavedMessage: &ent.ChannelMessage{ID: currentMessageID, ChannelID: channelID, Content: "那你明天把原因統整好之後告訴我", CreatedAt: messageTime},
	}, ClassificationResult{Tag: "todo", Signal: ClassificationSignalCandidate, Confidence: 0.9})

	if analyzer.dueTimeText != "明天" {
		t.Fatalf("expected due-time normalizer text to use due_text, got %q", analyzer.dueTimeText)
	}
	if !strings.Contains(analyzer.dueTimePrompt, "reference_time=2026-07-21T17:55:00+08:00") {
		t.Fatalf("expected due-time prompt to use message created_at as reference_time, got %q", analyzer.dueTimePrompt)
	}
	if persisted.DueAt == nil || !persisted.DueAt.Equal(expectedDueAt) {
		t.Fatalf("expected persisted due_at %s, got %+v", expectedDueAt.Format(time.RFC3339), persisted.DueAt)
	}
	if persisted.DueTimezone != "Asia/Taipei" || persisted.DuePrecision != "date" || persisted.DueDecision != "normalized" {
		t.Fatalf("unexpected persisted due-time fields: %+v", persisted)
	}
}

func TestTodoReminderServiceDoesNotPersistNoAction(t *testing.T) {
	// no_action 是 analyzer 明確判斷不要啟動 Todo Reminder；即使有 persistence function，也不能寫入候選表。
	channelID := uuid.New()
	repo := &stubRecentMessageStore{items: []*ent.ChannelMessage{
		{ID: uuid.New(), ChannelID: channelID, SenderName: "阿明", Content: "晚上吃什麼", CreatedAt: time.Now().Add(-time.Minute)},
	}}
	analyzer := &stubContextAnalyzer{todoResult: &llminteraction.TodoAnalysis{
		SchemaVersion: "v1",
		Decision:      "no_action",
		Confidence:    0.2,
		Reason:        "chat only",
	}}
	persistCalls := 0
	service := NewTodoReminderService(TodoReminderServiceOptions{
		Repo:          repo,
		LLM:           analyzer,
		PlatformLabel: "test",
		RecentLimit:   4,
		PersistTodoCandidate: func(ctx context.Context, input TodoCandidateInput) (*ent.TodoCandidate, error) {
			_ = ctx
			_ = input
			persistCalls++
			return &ent.TodoCandidate{ID: uuid.New()}, nil
		},
	})

	service.HandleClassification(context.Background(), MessageContext{
		Message:      &unifiedmessage.Message{ChannelID: "channel-1", PlatformMessageID: "m-2", MessageType: "text", Text: "哈哈好"},
		SavedMessage: &ent.ChannelMessage{ID: uuid.New(), ChannelID: channelID, Content: "哈哈好", CreatedAt: time.Now()},
	}, ClassificationResult{Tag: "todo", Signal: ClassificationSignalCandidate, Confidence: 0.9})

	if persistCalls != 0 {
		t.Fatalf("expected no_action to skip candidate persistence, got %d calls", persistCalls)
	}
}

func TestTodoReminderServiceAnalyzesExplicitReplyContextOutsideRecentWindow(t *testing.T) {
	// 顯式平台 reply/quote 是使用者直接指定的上下文，不應受 recent history window 限制；
	// todo reminder 應直接查 parent message，並把 parent 附近與目前訊息附近的 windows 依時間組合。
	channelID := uuid.New()
	currentMessageID := uuid.New()
	parentMessageID := uuid.New()
	recentMessageID := uuid.New()
	parentNearbyMessageID := uuid.New()
	baseTime := time.Now()
	repo := &stubRecentMessageStore{
		parent: &ent.ChannelMessage{ID: parentMessageID, ChannelID: channelID, SenderName: "主管", Content: "下個月一號前交預算表", CreatedAt: baseTime.Add(-24 * time.Hour)},
		windowsByAnchorID: map[uuid.UUID][]*ent.ChannelMessage{
			parentMessageID: {
				{ID: parentNearbyMessageID, ChannelID: channelID, SenderName: "會計", Content: "預算表要加上新版成本欄位", CreatedAt: baseTime.Add(-24*time.Hour + time.Minute)},
				{ID: parentMessageID, ChannelID: channelID, SenderName: "主管", Content: "下個月一號前交預算表", CreatedAt: baseTime.Add(-24 * time.Hour)},
			},
			currentMessageID: {
				{ID: recentMessageID, ChannelID: channelID, SenderName: "同事", Content: "剛剛主管說截止日提前一天", CreatedAt: baseTime.Add(-time.Minute)},
				{ID: currentMessageID, ChannelID: channelID, SenderName: "我", Content: "我晚點弄", CreatedAt: baseTime},
			},
		},
	}
	analyzer := &stubContextAnalyzer{}
	// ReplyChainMaxDepth 由 config 注入；測試明確給 4，避免 usecase 靠隱含常數追溯 nested reply。
	service := NewTodoReminderService(TodoReminderServiceOptions{Repo: repo, LLM: analyzer, PlatformLabel: "test", RecentLimit: 4, ReplyChainMaxDepth: 4})

	service.HandleClassification(context.Background(), MessageContext{
		Message:      &unifiedmessage.Message{ChannelID: "channel-1", PlatformMessageID: "m-2", ReplyToMsgID: "m-1", MessageType: "text", Text: "我晚點弄"},
		SavedMessage: &ent.ChannelMessage{ID: currentMessageID, ChannelID: channelID, ReplyToMsgID: "m-1", Content: "我晚點弄", CreatedAt: baseTime},
	}, ClassificationResult{Tag: "todo", Signal: ClassificationSignalCandidate})

	if repo.parentCalls != 1 {
		t.Fatalf("expected explicit reply parent query to be called once, got %d", repo.parentCalls)
	}
	if repo.windowCalls != 2 {
		t.Fatalf("expected explicit reply to query parent and current windows, got %d calls", repo.windowCalls)
	}
	if repo.calls != 0 {
		t.Fatalf("expected explicit reply to use message windows instead of recent query, got %d calls", repo.calls)
	}
	if analyzer.calls != 1 {
		t.Fatalf("expected explicit reply to call todo analyzer once, got %d calls", analyzer.calls)
	}
	if !strings.Contains(analyzer.prompt, "Explicit reply/quote target") || !strings.Contains(analyzer.prompt, parentMessageID.String()) || !strings.Contains(analyzer.prompt, "下個月一號前交預算表") {
		t.Fatalf("expected prompt to include explicit reply target, got %q", analyzer.prompt)
	}
	if !strings.Contains(analyzer.prompt, parentNearbyMessageID.String()) || !strings.Contains(analyzer.prompt, "預算表要加上新版成本欄位") {
		t.Fatalf("expected prompt to include parent message window context, got %q", analyzer.prompt)
	}
	if !strings.Contains(analyzer.prompt, recentMessageID.String()) || !strings.Contains(analyzer.prompt, "剛剛主管說截止日提前一天") {
		t.Fatalf("expected prompt to include current message window context, got %q", analyzer.prompt)
	}
}

func TestTodoReminderServiceCombinesNestedReplyWindowsByTime(t *testing.T) {
	// 被 reply 的訊息本身若又 reply 更早的訊息，todo reminder 會沿 reply chain 收集多個 window，
	// 去重後依 CreatedAt 排序，避免 analyzer 只看到最外層 parent 而漏掉原始交辦。
	channelID := uuid.New()
	currentMessageID := uuid.New()
	parentMessageID := uuid.New()
	grandParentMessageID := uuid.New()
	baseTime := time.Now()
	grandParent := &ent.ChannelMessage{ID: grandParentMessageID, ChannelID: channelID, SenderName: "主管", Content: "請整理年度預算", CreatedAt: baseTime.Add(-48 * time.Hour)}
	parent := &ent.ChannelMessage{ID: parentMessageID, ChannelID: channelID, ReplyToMsgID: "platform-grand-parent", SenderName: "同事", Content: "這份預算表要加 IT 成本", CreatedAt: baseTime.Add(-24 * time.Hour)}
	repo := &stubRecentMessageStore{
		parentByMessageID: map[uuid.UUID]*ent.ChannelMessage{
			currentMessageID: parent,
			parentMessageID:  grandParent,
		},
		windowsByAnchorID: map[uuid.UUID][]*ent.ChannelMessage{
			grandParentMessageID: {grandParent},
			parentMessageID:      {parent},
			currentMessageID:     {{ID: currentMessageID, ChannelID: channelID, Content: "我來補", CreatedAt: baseTime}},
		},
	}
	analyzer := &stubContextAnalyzer{}
	// ReplyChainMaxDepth 由 config 注入；測試明確給 4，才能驗證 nested reply 會繼續追到 grandparent window。
	service := NewTodoReminderService(TodoReminderServiceOptions{Repo: repo, LLM: analyzer, PlatformLabel: "test", RecentLimit: 4, ReplyChainMaxDepth: 4})

	service.HandleClassification(context.Background(), MessageContext{
		Message:      &unifiedmessage.Message{ChannelID: "channel-1", PlatformMessageID: "m-3", ReplyToMsgID: "m-2", MessageType: "text", Text: "我來補"},
		SavedMessage: &ent.ChannelMessage{ID: currentMessageID, ChannelID: channelID, ReplyToMsgID: "m-2", Content: "我來補", CreatedAt: baseTime},
	}, ClassificationResult{Tag: "todo", Signal: ClassificationSignalCandidate})

	if repo.parentCalls != 2 {
		t.Fatalf("expected current and parent reply links to be resolved, got %d", repo.parentCalls)
	}
	if repo.windowCalls != 3 {
		t.Fatalf("expected grandparent, parent, and current windows to be queried, got %d", repo.windowCalls)
	}
	contextSectionIndex := strings.Index(analyzer.prompt, "Context messages:")
	if contextSectionIndex < 0 {
		t.Fatalf("expected prompt to include context messages section, got %q", analyzer.prompt)
	}
	contextSection := analyzer.prompt[contextSectionIndex:]
	grandParentIndex := strings.Index(contextSection, grandParentMessageID.String())
	parentIndex := strings.Index(contextSection, parentMessageID.String())
	if grandParentIndex < 0 || parentIndex < 0 || grandParentIndex > parentIndex {
		t.Fatalf("expected nested reply windows to be merged by time, got %q", analyzer.prompt)
	}
}

func TestTodoReminderServiceSkipsImplicitReplyWhenRecentLimitNotConfigured(t *testing.T) {
	// recentLimit 的預設值屬於 config 層；usecase 不應再偷偷補 8。
	// 若測試或啟動流程沒有注入有效值，就直接略過 implicit linker，讓設定錯誤能在 log 中被看見。
	repo := &stubRecentMessageStore{}
	analyzer := &stubContextAnalyzer{}
	service := NewTodoReminderService(TodoReminderServiceOptions{Repo: repo, LLM: analyzer, PlatformLabel: "test"})

	service.HandleClassification(context.Background(), MessageContext{
		Message:      &unifiedmessage.Message{ChannelID: "channel-1", PlatformMessageID: "m-2", MessageType: "text", Text: "我晚點弄"},
		SavedMessage: &ent.ChannelMessage{ID: uuid.New(), ChannelID: uuid.New(), Content: "我晚點弄", CreatedAt: time.Now()},
	}, ClassificationResult{Tag: "todo", Signal: ClassificationSignalCandidate})

	if repo.calls != 0 {
		t.Fatalf("expected missing recent limit to skip recent message query, got %d calls", repo.calls)
	}
	if analyzer.calls != 0 {
		t.Fatalf("expected missing recent limit to skip todo analyzer, got %d calls", analyzer.calls)
	}
}

func TestTodoReminderServiceUsesRerankedImplicitReplyCandidates(t *testing.T) {
	// 驗證「召回窗口」和「語意排序」是兩階段：repository 先回時間序候選，
	// reranker 再依目前訊息把更相關的候選排前面，最後 prompt 必須使用精排後順序。
	channelID := uuid.New()
	olderMessageID := uuid.New()
	newerMessageID := uuid.New()
	repo := &stubRecentMessageStore{items: []*ent.ChannelMessage{
		{ID: olderMessageID, ChannelID: channelID, SenderName: "阿明", Content: "等等有人幫我買咖啡嗎", CreatedAt: time.Now().Add(-2 * time.Minute)},
		{ID: newerMessageID, ChannelID: channelID, SenderName: "小美", Content: "明天早上記得交報價單", CreatedAt: time.Now().Add(-time.Minute)},
	}}
	analyzer := &stubContextAnalyzer{}
	ranker := &stubImplicitReplyRanker{}
	service := NewTodoReminderService(TodoReminderServiceOptions{Repo: repo, LLM: analyzer, Ranker: ranker, PlatformLabel: "test", RecentLimit: 4})

	service.HandleClassification(context.Background(), MessageContext{
		Message:      &unifiedmessage.Message{ChannelID: "channel-1", PlatformMessageID: "m-3", MessageType: "text", Text: "我晚點補"},
		SavedMessage: &ent.ChannelMessage{ID: uuid.New(), ChannelID: channelID, Content: "我晚點補", CreatedAt: time.Now()},
	}, ClassificationResult{Tag: "todo", Signal: ClassificationSignalCandidate})

	if ranker.calls != 1 {
		t.Fatalf("expected reranker to be called once, got %d", ranker.calls)
	}
	firstIndex := strings.Index(analyzer.prompt, newerMessageID.String())
	secondIndex := strings.Index(analyzer.prompt, olderMessageID.String())
	if firstIndex < 0 || secondIndex < 0 || firstIndex > secondIndex {
		t.Fatalf("expected prompt to use reranked order, got %q", analyzer.prompt)
	}
}
