# Cortex Troubleshooting Guide

Common issues and solutions for Cortex users.

---

## Installation Issues

### Binary not found after install

**Problem**: `cortex: command not found`

**Solution**:
```bash
# Check if cortex is in your PATH
which cortex

# If not, add to PATH (add to ~/.bashrc or ~/.zshrc)
export PATH="$PATH:/usr/local/bin"

# Or reinstall with install script
./scripts/install.sh
```

### Permission denied

**Problem**: `permission denied: ./cortex`

**Solution**:
```bash
# Make binary executable
chmod +x cortex

# Or rebuild
go build -o cortex ./cmd/cortex
chmod +x cortex
```

---

## Ollama Issues

### Ollama not available

**Problem**: `Ollama not available, skipping analysis`

**Check**:
```bash
# Is Ollama running?
curl http://localhost:11434/api/tags

# If not, start it
ollama serve
```

**Solution**:
```bash
# Install Ollama
# macOS/Linux:
curl https://ollama.ai/install.sh | sh

# Or download from https://ollama.ai

# Pull recommended model
ollama pull mistral:7b
```

### Wrong model configured

**Problem**: `Model 'xyz' not found`

**Check available models**:
```bash
ollama list
```

**Solution**:
```bash
# Pull the configured model
ollama pull mistral:7b

# Or change config to use available model
# Edit .context/config.json
{
  "ollama_model": "mistral:7b"
}
```

---

## Capture Issues

### Events not being captured

**Problem**: No events appear in `.context/queue/pending/`

**Debug**:
```bash
# Check Claude Code hooks are configured
cat .claude/settings.local.json

# Should contain:
# "PostToolUse": [{"hooks": [{"command": "/path/to/cortex capture"}]}]

# Test capture manually
echo '{"tool_name":"Edit","tool_input":{},"tool_result":"test"}' | ./cortex capture

# Check queue
ls .context/queue/pending/
```

**Solution**:
```bash
# Reinitialize
./cortex init --auto

# Or manually fix hooks path
# Make sure path in .claude/settings.local.json is absolute
```

###  Slow capture (>50ms)

**Problem**: Capture taking too long, slowing down development

**Check logs**:
```bash
cat .context/logs/capture.log
```

**Solutions**:
1. **Disk is full**: Free up space
2. **Slow disk**: Move `.context/` to faster drive (SSD)
3. **Too many skip patterns**: Reduce patterns in config
4. **Antivirus scanning**: Exclude `.context/` directory

---

## Processing Issues

### Daemon not processing events

**Problem**: Events pile up in `queue/pending/`, never processed

**Debug**:
```bash
# Is daemon running?
ps aux | grep "cortex daemon"

# Check daemon logs (if you started with logging)
# cortex daemon > .context/logs/daemon.log 2>&1
```

**Solution**:
```bash
# Start daemon
./cortex daemon

# Or process queue manually once
./cortex process
```

### Daemon crashes

**Problem**: Daemon exits unexpectedly

**Debug**:
```bash
# Run with verbose logging
./cortex daemon 2>&1 | tee .context/logs/daemon.log

# Check for:
# - Database errors
# - Ollama connection issues
# - Out of memory
```

**Solutions**:
- **Database locked**: Stop duplicate daemons
- **Ollama down**: Start Ollama
- **Out of memory**: Reduce parallel workers (edit processor.go)

### No insights generated

**Problem**: `cortex insights` returns empty

**Check**:
```bash
# Are events being analyzed?
./cortex stats

# Should show total_insights > 0

# Check if Ollama is analyzing
# Watch daemon output for "Analyzed event..." messages
```

**Solutions**:
```bash
# Manually trigger analysis
./cortex analyze 10

# Check Ollama is working
curl http://localhost:11434/api/generate -d '{
  "model": "mistral:7b",
  "prompt": "test",
  "stream": false
}'
```

---

## Search Issues

### Search returns no results

**Problem**: `cortex search "query"` finds nothing

**Debug**:
```bash
# Are there events in database?
./cortex stats

# Check recent events
./cortex recent 10
```

**Solutions**:
- **No events captured**: Fix capture (see above)
- **Events not processed**: Run `./cortex process`
- **Query too specific**: Try broader search terms
- **Wrong project**: Check you're in correct directory

### Search is slow

**Problem**: Search takes >2 seconds

**Solutions**:
- **Large database**: This is expected with 10k+ events
- **Slow disk**: Move `.context/` to SSD
- **Future**: Vector search will be faster (planned feature)

---

## Context Injection Issues

### Context not injected into prompts

**Problem**: UserPromptSubmit hook not working

**Debug**:
```bash
# Check hook is configured
cat .claude/settings.local.json | grep UserPromptSubmit

# Test manually
echo "How should I implement auth?" | ./cortex inject-context
```

**Solutions**:
```bash
# Reinit to add missing hooks
./cortex init --auto

# Or manually add to .claude/settings.local.json:
"UserPromptSubmit": [{
  "hooks": [{
    "type": "command",
    "command": "/path/to/cortex inject-context"
  }]
}]
```

### Wrong context injected

**Problem**: Irrelevant insights appear

**Cause**: Simple keyword matching (current implementation)

**Workaround**:
- Use more specific search terms in prompts
- Delete irrelevant insights: Edit database manually (advanced)

**Future**: Semantic search will improve relevance (planned)

---

## Performance Issues

### High CPU usage

**Problem**: cortex daemon using >50% CPU

**Causes**:
- Analyzing many events simultaneously
- Ollama model too large for system

**Solutions**:
```bash
# Reduce parallel workers
# Edit internal/processor/processor.go:
# workersCh: make(chan struct{}, 2), // Reduce from 5 to 2

# Use smaller model
# Edit .context/config.json:
{
  "ollama_model": "phi3:mini"  // Faster, smaller
}
```

### High disk usage

**Problem**: `.context/` directory growing too large

**Check**:
```bash
du -sh .context/
du -sh .context/db/
du -sh .context/queue/
```

**Solutions**:
```bash
# Clean processed queue
rm -rf .context/queue/processed/*

# Compact database (careful!)
sqlite3 .context/db/events.db "VACUUM;"

# Reduce retention (future feature)
```

---

## Database Issues

### Database locked

**Problem**: `database is locked`

**Cause**: Multiple processes accessing database

**Solution**:
```bash
# Stop all cortex processes
killall cortex

# Restart daemon
./cortex daemon
```

### Corrupted database

**Problem**: `database disk image is malformed`

**Recovery**:
```bash
# Backup first!
cp .context/db/events.db .context/db/events.db.backup

# Try to recover
sqlite3 .context/db/events.db ".recover" | sqlite3 .context/db/events_recovered.db

# If that fails, start fresh (you'll lose history)
rm .context/db/events.db
./cortex init
```

---

## Claude Code Integration Issues

### Hooks not firing

**Problem**: PostToolUse/SessionStart hooks don't trigger

**Debug**:
```bash
# Check Claude Code version
# Hooks require recent version

# Check settings file syntax
cat .claude/settings.local.json | jq .

# If invalid JSON, fix syntax errors
```

**Solution**:
```bash
# Backup current settings
cp .claude/settings.local.json .claude/settings.local.json.backup

# Reinit (will regenerate)
./cortex init --auto
```

### Status line not showing

**Problem**: No Cortex status in Claude Code status bar

**Check**:
```bash
# Test status command
./cortex status

# Should output: 🤖🧠💡
```

**Solution**: Make sure `statusLine` is configured in `.claude/settings.local.json`

---

## Getting Help

### Enable Debug Logging

```bash
# Run daemon with logging
./cortex daemon 2>&1 | tee .context/logs/debug.log

# Check logs
tail -f .context/logs/debug.log
```

### Collect Diagnostic Info

```bash
# System info
./cortex info

# Stats
./cortex stats

# Recent events
./cortex recent 5

# Check configuration
cat .context/config.json
```

### Report an Issue

When reporting bugs, include:
1. Cortex version: `./cortex version`
2. OS and architecture
3. Ollama version and model
4. Error messages
5. Relevant logs from `.context/logs/`

**GitHub Issues**: https://github.com/dereksantos/cortex/issues

---

## Advanced Troubleshooting

### Manual Database Inspection

```bash
# Open database
sqlite3 .context/db/events.db

# Useful queries:
.schema                           # Show structure
SELECT COUNT(*) FROM events;      # Count events
SELECT COUNT(*) FROM insights;    # Count insights
SELECT * FROM events LIMIT 5;     # See recent events
SELECT * FROM insights ORDER BY created_at DESC LIMIT 5;

.quit
```

### Reset Everything

**⚠️ Warning: This deletes all captured context!**

```bash
# Backup first (optional)
cp -r .context .context.backup

# Delete context directory
rm -rf .context

# Reinitialize
./cortex init --auto
```

---

## Common Error Messages

| Error | Meaning | Solution |
|-------|---------|----------|
| `failed to load config` | No `.context/config.json` | Run `cortex init` |
| `Ollama not available` | Can't connect to Ollama | Start Ollama |
| `model not found` | Model not pulled | `ollama pull model-name` |
| `database is locked` | Multiple processes | Kill other cortex processes |
| `failed to create queue dir` | Permission error | Check directory permissions |
| `no data received` | Empty stdin | Check hook configuration |

---

**Still stuck?** Open an issue with debug info: https://github.com/dereksantos/cortex/issues
