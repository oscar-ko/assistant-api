# Configs Directory

Put all runtime/application config files under this directory.

Suggested future layout:

- `app.yml`: run mode, log, server, database, postgresql, line, and GraphQL runtime settings.
- `prompts.yml`: prompt templates or AI behavior config.
- `features.yml`: feature flags / rollout settings.
- `env/`: environment-specific overrides, e.g. `dev.yml`, `staging.yml`, `prod.yml`.

Current loader reads `configs/app.yml` by default.
Set `APP_CONFIG` environment variable to override config file path.
If `configs/app.local.yml` exists, it is merged after the main config and is intended for local-only secrets such as API tokens.

Recommended local override for secrets:

- Copy `configs/app.local.yml.example` to `configs/app.local.yml`
- Put local-only values such as `llm_providers.openai.token` there
- Keep `configs/app.local.yml` out of git

Database setup:

- Configure the `postgresql` section fields.
- Set `database.auto_schema_create` to control Ent automatic schema creation on startup.

LINE bind setup:

- Configure shared `line.redirect_uri`, then set each bot under `line.bots[]` with `channel_id`, `channel_secret`, `channel_token`, and `bot_user_id`.
- Open `/line/bind` to start OAuth bind flow.

