# Config Default Policy

## Purpose
Prevent default values from being scattered across runtime clients and business logic.

This repository uses a single source of truth for defaults:
- `internal/config/config.go` (`viper.SetDefault(...)`)
- `configs/app.yml` (runtime-visible values and comments)

## Rule
1. Default values must be defined only in config layer.
2. `usecase` clients (embedding/reranker/etc.) must not hardcode fallback defaults.
3. If required config is missing/invalid, client constructor should fail fast (return `nil` or error based on existing interface contract).

## Scope
Applies to:
- `internal/usecase/ai/embedding/*`
- `internal/usecase/ai/reranker/*`
- Any future external service clients (LLM, classifier, OCR, ASR, etc.)

## Allowed vs Disallowed
Allowed:
- Validation constants used only for sanity checks (example: `minPositiveDurationMS = 1`)
- `viper.SetDefault(...)` in `internal/config/config.go`

Disallowed:
- `if timeout <= 0 { timeout = 60 }` in client code
- `if path == "" { path = "/embed" }` in client code
- Any hidden fallback that bypasses config

## Constructor Contract
Client constructors should:
1. Validate all required config inputs.
2. Return `nil` (or error where contract allows) when inputs are invalid.
3. Never rewrite missing values into implicit defaults.

## Review Checklist
Before merge, verify:
1. New client/config knobs are added to `internal/config/config.go` defaults.
2. Same knobs are exposed in `configs/app.yml` with comments.
3. No fallback assignment exists in client constructor/runtime path.
4. Startup wiring passes config values directly into constructors.

## Quick Search Commands
Use these checks in review:

```powershell
# Find suspicious fallback patterns in AI clients
rg "<= 0 \{\s*.*=|== \"\" \{\s*.*=" internal/usecase/ai

# Verify defaults are centralized in config
rg "SetDefault\(" internal/config/config.go
```
