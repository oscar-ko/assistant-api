# Configs Directory

Put all runtime/application config files under this directory.

Suggested future layout:

- `app.yml`: run mode, log, server, database, postgresql, line, and GraphQL runtime settings.
- `prompts.yml`: prompt templates or AI behavior config.
- `features.yml`: feature flags / rollout settings.
- `env/`: environment-specific overrides, e.g. `dev.yml`, `staging.yml`, `prod.yml`.

Current loader reads `configs/app.yml` by default.
Set `APP_CONFIG` environment variable to override config file path.

執行模式：

- `run_mode: "dev"` 是目前唯一被程式明確辨識且有特殊行為的模式。它會啟用開發專用 GraphQL 模擬 resolver，並輸出 embedding/reranker 探活狀態轉換日誌。
- 任何非 `dev` 的值都會被視為一般非開發模式。目前尚未定義獨立的 `prod`、`staging` 或 `test` enum；只有在要關閉 dev-only 行為時才使用非 `dev` 值。

Secrets and provider tokens:

- Put runtime-only values such as `llm_providers.openai.token` or `llm_providers.deepseek.token` in the active config file (`configs/app.yml` by default, or the file selected by `APP_CONFIG`).
- Keep public examples such as `configs/app.example.yml` token-free.
- `configs/app.local.yml` is no longer loaded or used.

Database setup:

- Configure the `postgresql` section fields.
- Set `database.auto_schema_create` to control Ent automatic schema creation on startup.

LINE bind setup:

- Configure shared `line.redirect_uri`, then set each bot under `line.bots[]` with `channel_id`, `channel_secret`, `channel_token`, and `bot_user_id`.
- Open `/line/bind` to start OAuth bind flow.

