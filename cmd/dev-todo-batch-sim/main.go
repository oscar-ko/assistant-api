package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

type scriptMessage struct {
	Text                 string `json:"text"`
	ParticipantIndex     *int   `json:"participantIndex,omitempty"`
	ReplyToMessageIndex  *int   `json:"replyToMessageIndex,omitempty"`
	VisiblePlatformAppID string `json:"visiblePlatformAppID,omitempty"`
}

type graphQLRequest struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables"`
}

type graphQLResponse struct {
	Data   json.RawMessage `json:"data"`
	Errors []graphQLError  `json:"errors"`
}

type graphQLError struct {
	Message string `json:"message"`
}

type clearData struct {
	ClearDevRealtimeTodoData map[string]any `json:"clearDevRealtimeTodoData"`
}

type simulateData struct {
	SimulateTodoConversation simulatePayload `json:"simulateTodoConversation"`
}

type simulatePayload struct {
	Status             string             `json:"status"`
	TodoCandidateCount int                `json:"todoCandidateCount"`
	Messages           []simulatedMessage `json:"messages"`
}

type simulatedMessage struct {
	Text                     string  `json:"text"`
	PlatformMessageID        string  `json:"platformMessageID"`
	VisiblePlatformMessageID *string `json:"visiblePlatformMessageID"`
	SavedMessageID           *string `json:"savedMessageID"`
	TodoCandidateID          *string `json:"todoCandidateID"`
	TodoCandidateStatus      *string `json:"todoCandidateStatus"`
	TodoCandidateDecision    *string `json:"todoCandidateDecision"`
	TodoCandidateSummary     *string `json:"todoCandidateSummary"`
}

const clearQuery = `mutation Clear($input: ClearDevRealtimeTodoDataInput!) {
  clearDevRealtimeTodoData(input: $input) {
    status
    todoCount
    todoEventCount
    todoUpdateCandidateCount
    todoCandidateCount
    todoCandidateAssigneeCount
    todoCandidateEvidenceMessageCount
    channelMessageCount
  }
}`

const simulateQuery = `mutation SimulateBatch($input: SimulateTodoConversationInput!) {
  simulateTodoConversation(input: $input) {
    status
    todoCandidateCount
    messages {
      text
      platformMessageID
      visiblePlatformMessageID
      savedMessageID
      todoCandidateID
      todoCandidateStatus
      todoCandidateDecision
      todoCandidateSummary
    }
  }
}`

func main() {
	endpoint := flag.String("endpoint", "http://127.0.0.1:8080/query", "GraphQL endpoint")
	channelID := flag.String("channel", "8d7f7c12-a998-47a4-a12a-8f4c7fbfe556", "system channel id")
	platformTenantID := flag.String("team", "T017LF31KBM", "Slack team id")
	platformAppID := flag.String("app", "A0BJ1BJFNQH", "default Slack app id")
	deliveryMode := flag.String("delivery", "visible", "delivery mode: internal or visible")
	participantCount := flag.Int("participants", 2, "number of simulated conversation participants")
	batchSize := flag.Int("batch", 50, "messages per GraphQL request")
	totalMessages := flag.Int("total", 200, "total script messages to generate")
	analysisWait := flag.Int("wait", 700, "analysis wait milliseconds per message")
	skipClear := flag.Bool("skip-clear", false, "do not clear dev realtime Todo data before running")
	skipProbe := flag.Bool("skip-probe", false, "do not run cmd/dev-todo-probe after simulation")

	printScript := flag.Bool("print-script", false, "print generated simulation messages and exit")
	flag.Parse()
	mode := strings.ToLower(strings.TrimSpace(*deliveryMode))
	if mode != "internal" && mode != "visible" {
		exitError(errors.New("delivery must be internal or visible"))
	}
	if *batchSize < 1 {
		exitError(errors.New("batch must be at least 1"))
	}
	if *totalMessages < 1 {
		exitError(errors.New("total must be at least 1"))
	}
	if *participantCount < 1 {
		exitError(errors.New("participants must be at least 1"))
	}

	ctx := context.Background()
	client := &http.Client{Timeout: 15 * time.Minute}
	messages := buildMessages(*totalMessages, *participantCount)
	if *printScript {
		printJSON(messages)
		return
	}

	if !*skipClear {
		fmt.Printf("Clearing dev realtime Todo data in channel %s\n", *channelID)
		var cleared clearData
		if err := invokeGraphQL(ctx, client, *endpoint, clearQuery, map[string]any{"input": map[string]any{"channelID": *channelID}}, &cleared); err != nil {
			exitError(err)
		}
		printJSON(cleared.ClearDevRealtimeTodoData)
	}

	basePlatformMessageIDs := make([]string, 0, len(messages))
	baseVisiblePlatformMessageIDs := make([]string, 0, len(messages))
	for offset := 0; offset < len(messages); offset += *batchSize {
		end := offset + *batchSize
		if end > len(messages) {
			end = len(messages)
		}
		input := map[string]any{
			"channelID":                *channelID,
			"platform":                 "slack",
			"platformTenantID":         *platformTenantID,
			"platformAppID":            *platformAppID,
			"deliveryMode":             mode,
			"participantCount":         *participantCount,
			"messageCount":             end - offset,
			"analysisWaitMilliseconds": *analysisWait,
			"messages":                 messages[offset:end],
			"basePlatformMessageIDs":   append([]string(nil), basePlatformMessageIDs...),
		}
		if mode == "visible" {
			input["baseVisiblePlatformMessageIDs"] = append([]string(nil), baseVisiblePlatformMessageIDs...)
		}

		fmt.Printf("Running batch %d-%d / %d\n", offset+1, end, len(messages))
		var simulated simulateData
		if err := invokeGraphQL(ctx, client, *endpoint, simulateQuery, map[string]any{"input": input}, &simulated); err != nil {
			exitError(err)
		}
		for _, message := range simulated.SimulateTodoConversation.Messages {
			basePlatformMessageIDs = append(basePlatformMessageIDs, strings.TrimSpace(message.PlatformMessageID))
			if mode == "visible" {
				visibleID := ""
				if message.VisiblePlatformMessageID != nil {
					visibleID = strings.TrimSpace(*message.VisiblePlatformMessageID)
				}
				baseVisiblePlatformMessageIDs = append(baseVisiblePlatformMessageIDs, visibleID)
			}
		}
		fmt.Printf("Batch complete. Accumulated messages: %d; candidate count: %d\n", len(basePlatformMessageIDs), simulated.SimulateTodoConversation.TodoCandidateCount)
	}

	if !*skipProbe {
		fmt.Println("Running DB probe")
		cmd := exec.CommandContext(ctx, "go", "run", ".\\cmd\\dev-todo-probe", "-channel", *channelID)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			exitError(fmt.Errorf("run dev-todo-probe failed: %w", err))
		}
	}
}

func buildMessages(total int, participantCount int) []scriptMessage {
	botApps := []string{"A0BJ1BJFNQH", "A0BJXJE37CN", "A0BJJHNDCQ7"}
	messages := make([]scriptMessage, 0, total)
	add := func(text string, replyTo int) {
		participantIndex := len(messages) % participantCount
		message := scriptMessage{Text: text, ParticipantIndex: &participantIndex, VisiblePlatformAppID: botApps[len(messages)%len(botApps)]}
		if replyTo >= 0 {
			message.ReplyToMessageIndex = &replyTo
		}
		messages = append(messages, message)
	}
	for index := 0; index < total; index++ {
		switch index {
		case 0:
			add("Oscar，請整理本週客服回報的三個主要問題，做成待辦追蹤。", -1)
		case 18:
			add("客服回報整理那件事，負責人就是 Oscar，先把問題分類跟影響範圍列清楚。", -1)
		case 24:
			add("這份客服整理請在明天下午五點前完成，先給我一版摘要。", -1)
		case 70:
			add("回覆前面那個客服整理：請把高頻問題、影響客戶數、目前處理狀態拆成三欄。", 0)
		case 88:
			add("Oscar，請另外建立一份合約缺漏清單，把法務需要補的欄位整理出來。", -1)
		case 96:
			add("合約缺漏清單這件事由 Oscar 負責，明天中午前先交第一版。", -1)
		case 119:
			add("客服整理要把重複回報合併，不要逐條貼，最後留一份可追蹤的摘要。", -1)
		case 150:
			add("回覆合約缺漏清單：請把付款條件、簽核人、附件缺漏分成不同段落。", 88)
		case 181:
			add("合約缺漏清單確認仍是明天中午前，Oscar 先整理第一版給我看。", 88)
		default:
			add(noiseText(index), -1)
		}
	}
	return messages
}

func noiseText(index int) string {
	items := []string{
		"剛剛會議室冷氣好像忽冷忽熱，晚點再請行政看一下。",
		"午餐我先跳過，下午還有一個客戶電話。",
		"staging 健康檢查目前看起來正常，錯誤率沒有升高。",
		"設計稿的按鈕顏色看起來比上一版柔和。",
		"資料庫備份排程今天凌晨有成功完成。",
		"新的 ngrok URL 我放在內部文件，不要貼到公開地方。",
		"同步會議議程目前維持原本順序。",
		"白板照片已放到共用資料夾，檔名照日期排序。",
		"產品頁文案目前先維持上一版內容。",
		"今天 Slack 通知有一點延遲，不過事件應該都有送到。",
	}
	return fmt.Sprintf("%s（插話 %03d）", items[index%len(items)], index)
}

func invokeGraphQL(ctx context.Context, client *http.Client, endpoint string, query string, variables map[string]any, target any) error {
	payload, err := json.Marshal(graphQLRequest{Query: query, Variables: variables})
	if err != nil {
		return fmt.Errorf("marshal GraphQL request failed: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("build GraphQL request failed: %w", err)
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("send GraphQL request failed: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read GraphQL response failed: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("GraphQL HTTP status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var envelope graphQLResponse
	if err := json.Unmarshal(body, &envelope); err != nil {
		return fmt.Errorf("decode GraphQL response failed: %w", err)
	}
	if len(envelope.Errors) > 0 {
		parts := make([]string, 0, len(envelope.Errors))
		for _, graphErr := range envelope.Errors {
			parts = append(parts, graphErr.Message)
		}
		return fmt.Errorf("GraphQL returned errors: %s", strings.Join(parts, "; "))
	}
	if target == nil {
		return nil
	}
	if err := json.Unmarshal(envelope.Data, target); err != nil {
		return fmt.Errorf("decode GraphQL data failed: %w", err)
	}
	return nil
}

func printJSON(value any) {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		fmt.Printf("%v\n", value)
		return
	}
	fmt.Println(string(encoded))
}

func exitError(err error) {
	fmt.Fprintf(os.Stderr, "error: %v\n", err)
	os.Exit(1)
}
