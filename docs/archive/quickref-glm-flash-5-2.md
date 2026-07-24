# Quick Reference: GLM Flash 5.2 with OpenRouter

## TL;DR

```bash
export OPEN_ROUTER_API_KEY="sk-or-v1-..."
cat > ~/.cortex/config.json <<'EOF'
{
  "backend": {
    "type": "openrouter",
    "endpoint": "https://openrouter.ai/api/v1",
    "key_env": "OPEN_ROUTER_API_KEY"
  },
  "models": {
    "code": {"model": "zhipu/glm-flash-5-2"}
  }
}
EOF
./bin/cortex
```

## Model ID on OpenRouter

**`zhipu/glm-flash-5-2`**

Format: `provider/model-name`

## Configuration Summary

| Setting | Value |
|---------|-------|
| Backend Type | `openrouter` |
| Endpoint | `https://openrouter.ai/api/v1` |
| Model ID | `zhipu/glm-flash-5-2` |
| Context Window | 1M tokens |
| API Key Env | `OPEN_ROUTER_API_KEY` |

## Common Commands

```bash
# Get API key
open https://openrouter.ai/keys

# Set key (one-time per session)
export OPEN_ROUTER_API_KEY="sk-or-v1-..."

# Verify key works
curl https://openrouter.ai/api/v1/models \
  -H "Authorization: Bearer $OPEN_ROUTER_API_KEY" | jq

# Start Cortex
./bin/cortex

# Check current model (in REPL)
/model

# Switch models (in REPL)
/model zhipu/glm-flash-5-2

# List all models (in REPL)
/models
```

## Config File Locations

| File | Purpose | Priority |
|------|---------|----------|
| `~/.cortex/config.json` | User-wide settings | Lower |
| `./.cortex/config.json` | Project-specific | Higher |

## Troubleshooting

| Error | Fix |
|-------|-----|
| `OPEN_ROUTER_API_KEY not set` | `export OPEN_ROUTER_API_KEY=...` |
| `no LLM client available` | Verify API key with curl |
| `model not found` | Check `zhipu/glm-flash-5-2` exists on OpenRouter |

## Related

- Full guide: `docs/openrouter-glm-flash-5-2.md`
- OpenRouter docs: https://openrouter.ai/docs
- GLM Flash 5.2: https://openrouter.ai/models/zhipu/glm-flash-5-2
