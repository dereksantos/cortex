# Using GLM Flash 5.2 with Cortex via OpenRouter

This guide explains how to configure Cortex to use Zhipu's GLM Flash 5.2 model through the OpenRouter gateway.

## Prerequisites

1. **Go 1.25+** installed
2. **OpenRouter API key** from https://openrouter.ai/keys

## Step-by-Step Setup

### 1. Build Cortex

```bash
go build -o bin/loop ./cmd/loop
```

### 2. Set Your OpenRouter API Key

```bash
export OPEN_ROUTER_API_KEY="sk-or-v1-xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"
```

For persistent setup, add this to your shell profile:
- **bash**: `~/.bashrc`
- **zsh**: `~/.zshrc`
- **fish**: `~/.config/fish/config.fish`

### 3. Create Configuration File

Create `~/.cortex/config.json` (or `.cortex/config.json` in your project):

```json
{
  "backend": {
    "type": "openrouter",
    "endpoint": "https://openrouter.ai/api/v1",
    "key_env": "OPEN_ROUTER_API_KEY"
  },
  "models": {
    "code": {
      "model": "zhipu/glm-flash-5-2",
      "window": 1048576
    },
    "study": {
      "model": "zhipu/glm-flash-5-2"
    }
  },
  "temperature": 0.7
}
```

**Configuration options explained:**

- `backend.type`: Set to `"openrouter"` to use the OpenRouter gateway
- `backend.endpoint`: OpenRouter's API endpoint (default: `https://openrouter.ai/api/v1`)
- `backend.key_env`: Environment variable containing your API key (default: `"OPEN_ROUTER_API_KEY"`)
- `models.code.model`: The model to use for coding tasks (the main agent)
- `models.study.model`: The model to use for study/compaction tasks
- `temperature`: Global temperature override (optional, default: 1.0)

### 4. Verify Configuration

Start Cortex REPL:

```bash
./bin/loop
```

In the REPL, check available models:

```
/models
```

You should see `zhipu/glm-flash-5-2` in the list.

### 5. Switch to GLM Flash 5.2 (if needed)

If you already have Cortex running with a different model:

```
/model zhipu/glm-flash-5-2
```

## Advanced Configuration

### Separate Models for Different Roles

You can use different models for coding and study tasks:

```json
{
  "backend": {
    "type": "openrouter",
    "endpoint": "https://openrouter.ai/api/v1",
    "key_env": "OPEN_ROUTER_API_KEY"
  },
  "models": {
    "code": {
      "model": "zhipu/glm-flash-5-2"
    },
    "study": {
      "model": "openai/gpt-oss-20b:free"
    }
  }
}
```

### Custom Context Window

If you need to override the context window size:

```json
{
  "models": {
    "code": {
      "model": "zhipu/glm-flash-5-2",
      "window": 131072
    }
  }
}
```

### Using API Key Directly (Not Recommended)

For development only, you can embed the key in config:

```json
{
  "backend": {
    "type": "openrouter",
    "endpoint": "https://openrouter.ai/api/v1",
    "key_env": "OPEN_ROUTER_API_KEY"
  }
}
```

And set `OPEN_ROUTER_API_KEY` in your environment.

## Model Details

**GLM Flash 5.2 on OpenRouter:**
- Model ID: `zhipu/glm-flash-5-2`
- Provider: Zhipu (GLM series)
- Context Window: Up to 1M tokens
- Best for: Fast coding tasks, general reasoning

## Troubleshooting

### "OPEN_ROUTER_API_KEY not set"

Make sure you've set the environment variable:
```bash
echo $OPEN_ROUTER_API_KEY
```

### "no LLM client available"

Verify your API key is valid:
```bash
curl -H "Authorization: Bearer $OPEN_ROUTER_API_KEY" https://openrouter.ai/api/v1/models | jq
```

### "model not found"

Check available models:
```bash
curl -H "Authorization: Bearer $OPEN_ROUTER_API_KEY" https://openrouter.ai/api/v1/models | jq '.data[] | select(.id | contains("glm"))'
```

### Wrong model being used

In the REPL, verify the current model:
```
/model
```

Switch if needed:
```
/model zhipu/glm-flash-5-2
```

## Environment Variables

| Variable | Description | Default |
|----------|-------------|---------|
| `OPEN_ROUTER_API_KEY` | Your OpenRouter API key | Required |
| `CORTEX_BACKEND` | Override backend endpoint | - |
| `CORTEX_HOME` | Custom config directory | `~/.cortex` |
| `CORTEX_LOOP_RENDER` | Disable markdown rendering | - |

## Examples

### Minimal Config (Project-Specific)

Create `.cortex/config.json` in your project:

```json
{
  "models": {
    "code": {
      "model": "zhipu/glm-flash-5-2"
    }
  }
}
```

This overrides only the model, keeping other settings from user config or defaults.

### Multi-Model Setup

```json
{
  "backend": {
    "type": "openrouter",
    "key_env": "OPEN_ROUTER_API_KEY"
  },
  "models": {
    "code": {
      "model": "zhipu/glm-flash-5-2",
      "temperature": 0.7
    },
    "study": {
      "model": "anthropic/claude-3.5-sonnet",
      "temperature": 0.5
    },
    "fast": {
      "model": "openai/gpt-oss-20b:free"
    }
  }
}
```

## Next Steps

After configuration:

1. Start using Cortex: `./bin/loop`
2. Try the study tool: `/study <file-or-directory>`
3. Read files: `read_file(<path>)`
4. Edit files with the agent: make changes directly

## Related Documentation

- [Cortex README](../README.md)
- [Cortex Configuration](../CLAUDE.md#configuration)
- [OpenRouter Models](https://openrouter.ai/models)
- [GLM Flash 5.2](https://openrouter.ai/models/zhipu/glm-flash-5-2)
