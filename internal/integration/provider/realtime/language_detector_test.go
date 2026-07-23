package realtime

import (
	"context"
	"testing"
)

func TestWhatlangLanguageDetectorDetectsLongChineseMessage(t *testing.T) {
	// 鎖住 20-30 個中文字左右的常見群聊訊息，確保來源語言可可靠辨識，
	// 才能讓 AutoTranslateService 正確排除 zh/zh-TW 這類同語言目標。
	detector := NewWhatlangLanguageDetector()

	got, err := detector.DetectLanguage(context.Background(), "今天下午三點要開會，請大家先把報告整理好，並確認客戶需求是否更新。")
	if err != nil {
		t.Fatalf("DetectLanguage() error = %v", err)
	}
	if got != "zh" {
		t.Fatalf("DetectLanguage() = %q, want %q", got, "zh")
	}
}

func TestWhatlangLanguageDetectorDetectsLongEnglishMessage(t *testing.T) {
	// 鎖住 30-40 個英文單字左右的長訊息情境，避免英文長文被誤判後，
	// 導致同語言過濾失效或錯刪其他目標語系。
	detector := NewWhatlangLanguageDetector()

	got, err := detector.DetectLanguage(context.Background(), "Please review the customer proposal before tomorrow morning and make sure the pricing assumptions, delivery timeline, and support responsibilities are all clearly documented for the team.")
	if err != nil {
		t.Fatalf("DetectLanguage() error = %v", err)
	}
	if got != "en" {
		t.Fatalf("DetectLanguage() = %q, want %q", got, "en")
	}
}
