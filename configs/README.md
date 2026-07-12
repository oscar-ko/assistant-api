# Configs Directory

Put all runtime/application config files under this directory.

Suggested future layout:

- `app.yml`: run mode, log, server, database, postgresql, line, and GraphQL runtime settings.
- `prompts.yml`: prompt templates or AI behavior config.
- `features.yml`: feature flags / rollout settings.
- `env/`: environment-specific overrides, e.g. `dev.yml`, `staging.yml`, `prod.yml`.

Current loader reads `configs/app.yml` by default.
Set `APP_CONFIG` environment variable to override config file path.

Database setup:

- Configure the `postgresql` section fields.
- Set `database.auto_schema_create` to control Ent automatic schema creation on startup.

LINE bind setup:

- Configure `line.channel_id`, `line.client_secret`, and `line.redirect_uri`.
- Open `/line/bind` to start OAuth bind flow.
