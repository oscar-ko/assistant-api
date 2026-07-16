# AI 目錄與命名規範

本文件定義 assistant-api 在 AI 相關程式碼上的分層、目錄切分與檔名規範。

目標：
- 保持 local LLM 與 cloud LLM 可互相替換。
- 業務邏輯只維護一份，不因 provider 分叉。
- provider 差異集中在 integration 層。

## 一、分層原則

1. `usecase` 只放能力與流程，不放供應商名稱。
2. `integration/provider` 放供應商實作與網路 I/O。
3. `orchestrator` 放路由策略（何時走 local / cloud），不放供應商細節。

## 二、目錄規範

### 2.1 Usecase（能力導向）

建議目錄：

```text
internal/usecase/ai/
  llm_interaction/      # 問答、語意判斷、action decision 的任務流程
  llm_orchestrator/     # local/cloud/hybrid 路由策略
  retrieval/            # 候選召回流程（可由現有 topkfilter 漸進整併）
  ranking/              # 候選重排流程
```

目前專案可保留：
- `embedding/`
- `topkfilter/`
- `reranker/`

上述目錄可在不改行為前提下，逐步收斂到 retrieval/ranking 能力命名。

### 2.2 Integration（供應商導向）

建議目錄：

```text
internal/integration/provider/
  llm/
    openai/
    local/
    anthropic/
  embedding/
    <provider>/
  reranker/
    <provider>/
```

如果暫時不搬目錄，至少保持命名語意一致：
- `provider/openai` 只放 OpenAI 實作
- `provider/line` 只放 LINE 實作

## 三、命名規範

### 3.1 Package 命名

1. 一律使用能力或角色命名。
2. usecase 禁止使用 `openai`、`chatgpt` 這類供應商名。
3. 供應商名僅能出現在 `integration/provider/<vendor>`。

範例：
- 好：`llminteraction`, `llmorchestrator`
- 避免：`chatgpt`, `openai`（出現在 usecase 時）

### 3.2 檔名命名

每個 package 優先使用下列固定檔名：

1. `service.go`
- 該 package 的主要流程入口。

2. `port.go`
- 對外介面與對下游依賴介面（可搭配 DI 使用）。

3. `types.go`
- request/response 與共用型別。

4. `errors.go`
- domain error 與錯誤判別 helper。

5. `<task>.go`
- 任務拆分檔，例如 `action_decision.go`、`question_answer.go`。

6. `<name>_test.go`
- 與被測檔案同語意命名，便於對照。

### 3.3 Provider 檔名

provider package 建議固定：

1. `client.go`
- 封裝 HTTP/SDK 呼叫。

2. `mapper.go`
- 供應商 payload 與 domain 型別互轉。

3. `config.go`
- provider 專屬設定與驗證。

## 四、local/cloud 的切分規則

local 與 cloud 是平行執行域，不是主從關係。

1. 業務契約相同時：
- 不要拆兩份 usecase。
- 使用同一份 usecase + 不同 provider adapter。

2. 只有下列情況才拆分兩份流程：
- 輸出契約不同（例如某端不支援結構化 JSON contract）。
- 工具能力不同且無法在 adapter 吸收（例如一端有 tool use，另一端完全沒有）。
- SLA 或合規限制要求獨立流程（例如資料不可出境）。

## 五、組裝與設定

1. `app` 層負責注入：
- 選擇 local / cloud / hybrid。
- 設定 timeout、重試、策略門檻。

2. `config` 層管理 default 與必要欄位：
- default 只能在 config 層定義。
- provider constructor 缺必要設定時要 fail fast。

## 六、現況到目標的最小落地步驟

1. 保留現有 `llm_interaction` 與 `llm_orchestrator`。
2. usecase 內若出現供應商名稱，改為能力名稱。
3. provider 內維持供應商命名，集中網路呼叫。
4. 新功能先依本文件建 package，不再新增供應商命名 usecase。

---

本文件優先保證「可替換性與單一業務邏輯」，再追求目錄收斂；
若需大規模搬移目錄，建議以功能不變的重構 PR 分批進行。
