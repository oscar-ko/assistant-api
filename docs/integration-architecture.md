# Integration 檔案架構規劃

此文件記錄 OAuth 與外部整合能力的建議分層，目標是支援：
- Google OAuth
- Gmail 存取
- Calendar 存取
- LINE OAuth
- 其他 Chat/LLM 供應商（如 OpenAI、Anthropic）

## 設計目標

1. 將「登入/綁定」與「能力存取（mail/calendar/chat）」分離。
2. 將 provider 差異收斂在 adapter，不污染業務流程。
3. 以 capability 為中心擴充，不以單一供應商為中心複製程式。
4. 集中管理 token/credential 與 scope 授權。

## 建議目錄

```text
internal/
  integration/
    core/                      # registry、capability model、統一錯誤與事件
    auth/                      # OAuth/OIDC 共用流程（state、token exchange、refresh）
    credential/                # token/API key 儲存、加密、輪替
    provider/
      google/                  # Google provider adapter（僅 provider 特有邏輯）
      microsoft/
      openai/
      anthropic/
      line/
    capability/
      mail/                    # Gmail/Outlook 共用 mail 介面
      calendar/                # Google Calendar/Outlook Calendar 共用介面
      chat/                    # OpenAI/Anthropic/Slack/Teams 共用介面
    webhook/                   # 各 provider webhook 驗簽與事件轉換
    job/                       # sync、watch renew、retry/backoff
```

## 路由建議

統一路由規則：
- `/auth/{provider}/start`
- `/auth/{provider}/callback`

靜態頁面建議：
- `static/auth/line/bind.html`
- `static/auth/google/bind.html`
- `static/auth/common/success.html`
- `static/auth/common/error.html`

## 資料模型建議

1. `connection`：使用者與 provider 的連線狀態。
2. `credential`：access token、refresh token、expiry（需加密儲存）。
3. `grant`：連線授予的 capability/scope。
4. `sync_state`：mail/calendar 同步游標（如 historyId、syncToken）。
5. `webhook_subscription`：watch channel、到期時間、續約資訊。

## Scope 與能力映射

建議以 capability 驅動 scope，不在流程中硬編碼：
- capability 宣告自己需要的授權範圍
- provider adapter 做 provider-specific scope 映射
- 啟動 OAuth 時由 capability 組合出 scopes

## 漸進落地方式

1. 先維持既有 LINE 功能可用。
2. 抽出 auth 共用模組（state、callback 驗證、錯誤模型）。
3. 將 LINE provider 完整放入 `internal/integration/provider/line`。
4. 新增 Google provider 骨架，再逐步接上 Gmail/Calendar capability。
5. 最後將舊路由切換到統一路由規則。

## 開發約定

為避免設定來源分散與行為不一致，請遵守以下規則：

1. default 僅可定義在 config 層（`internal/config/config.go` 與 `configs/app.yml`）。
2. service client（embedding/reranker/未來其他 provider client）不得在程式內做 fallback default。
3. 若必要設定缺失，constructor 應 fail fast，不得靜默改寫成隱藏預設值。

詳細規範與 code review 檢查項請參考：
- [docs/config-default-policy.md](docs/config-default-policy.md)
