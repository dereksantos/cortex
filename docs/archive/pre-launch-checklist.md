# Cortex Pre-Launch Checklist

**Goal**: Dogfood Cortex for 2 weeks to validate functionality, find issues, and ensure quality before public release.

**Timeline**: 14 days of real-world usage

**Track Progress**: Check off items as you complete them

---

## 📅 Week 1: Installation & Basic Usage

### Day 1-2: Fresh Install Testing

- [ ] **Test install on fresh machine** (VM or Docker)
  - [ ] Clone repo on clean system
  - [ ] Run `./scripts/install.sh`
  - [ ] Verify binary in PATH
  - [ ] Test `cortex version`

- [ ] **Verify prerequisites**
  - [ ] Ollama installed and running
  - [ ] Model downloaded (`mistral:7b`)
  - [ ] Go 1.21+ available

- [ ] **Test auto-setup**
  - [ ] `cd` into test project
  - [ ] Run `cortex init --auto`
  - [ ] Verify `.cortex/` created
  - [ ] Check `.claude/settings.local.json` updated
  - [ ] Verify all hooks added (PostToolUse, SessionStart, UserPromptSubmit)

- [ ] **Verify daemon starts**
  - [ ] Run `cortex daemon`
  - [ ] Check process running
  - [ ] Verify no errors in logs
  - [ ] Test graceful shutdown (Ctrl+C)

**Questions to Answer:**
- Does installation "just work"?
- Are error messages helpful?
- What confused you?

---

### Day 3-4: Basic Capture & Retrieval

- [ ] **Test event capture**
  - [ ] Make code changes with Claude Code
  - [ ] Verify events captured (check `.cortex/queue/pending/`)
  - [ ] Confirm daemon processes events
  - [ ] Check events in database (`cortex stats`)

- [ ] **Test search functionality**
  - [ ] Run `cortex search "your query"`
  - [ ] Verify results returned
  - [ ] Test with no results
  - [ ] Test with special characters

- [ ] **Test insights**
  - [ ] Wait for LLM analysis (5-10 minutes)
  - [ ] Run `cortex insights`
  - [ ] Check insight quality
  - [ ] Verify categories correct (decision/pattern/insight)

- [ ] **Test new hooks**
  - [ ] Start new Claude Code session → session-start instructions appear?
  - [ ] Submit prompt → context injected?
  - [ ] Verify context relevance

**Metrics to Track:**
- Capture time: < 10ms? (check logs)
- Search time: < 500ms?
- Insight quality: Useful? Accurate?
- Context injection: Helpful or noisy?

---

### Day 5-7: Real Development Work

- [ ] **Use on actual project**
  - [ ] Make real architectural decisions
  - [ ] Implement features with patterns
  - [ ] Write tests
  - [ ] Refactor code

- [ ] **Test decision recall**
  - [ ] Make architectural choice (database, framework, etc.)
  - [ ] Wait 2 days
  - [ ] Search for it: `cortex search "database choice"`
  - [ ] Was it found easily?

- [ ] **Test pattern consistency**
  - [ ] Establish pattern (error handling, logging, etc.)
  - [ ] Use pattern multiple times
  - [ ] Check `cortex entities pattern`
  - [ ] Verify patterns tracked

- [ ] **Test deduplication**
  - [ ] Edit same file 5 times in 30 seconds
  - [ ] Check insights generated
  - [ ] Should only analyze once (not 5 times)

- [ ] **Test filtering**
  - [ ] Edit binary files (.png, .jpg)
  - [ ] Edit lock files
  - [ ] Verify NOT analyzed
  - [ ] Edit .md, .go files → should analyze

**Questions to Answer:**
- Did Cortex actually help you recall decisions?
- Were insights high quality or mostly noise?
- Did deduplication work correctly?
- Any false positives/negatives in filtering?

---

## 📅 Week 2: Edge Cases & Polish

### Day 8-10: Stress Testing

- [ ] **Test large changes**
  - [ ] Edit file with >1000 lines changed
  - [ ] Verify capture completes
  - [ ] Check insight quality

- [ ] **Test rapid edits**
  - [ ] Make 20 edits in 1 minute
  - [ ] Verify queue doesn't overflow
  - [ ] Check daemon keeps up

- [ ] **Test Ollama failures**
  - [ ] Stop Ollama service
  - [ ] Make code changes
  - [ ] Verify graceful degradation
  - [ ] Restart Ollama
  - [ ] Check processing resumes

- [ ] **Test disk space**
  - [ ] Fill disk to 95%
  - [ ] Verify errors logged (not crashed)
  - [ ] Check queue behavior

- [ ] **Test process crashes**
  - [ ] Kill daemon mid-analysis (kill -9)
  - [ ] Restart daemon
  - [ ] Verify no data loss
  - [ ] Check queue recovery

**Metrics to Track:**
- Max events/second captured
- Queue max size before slowdown
- Daemon recovery time
- Data integrity after crashes

---

### Day 11-12: Advanced Features

- [ ] **Test knowledge graph**
  - [ ] Run `cortex entities`
  - [ ] Run `cortex graph decision "some decision"`
  - [ ] Verify relationships correct
  - [ ] Check graph traversal works

- [ ] **Test multi-project**
  - [ ] Init Cortex in 2 different projects
  - [ ] Verify contexts separate
  - [ ] No cross-contamination

- [ ] **Test context injection deeply**
  - [ ] Ask question about past decision
  - [ ] Check if relevant context injected
  - [ ] Verify context helps Claude answer better
  - [ ] Test with no relevant context (should pass through unchanged)

- [ ] **Performance validation**
  - [ ] Measure capture time (add timing logs)
  - [ ] Confirm < 10ms
  - [ ] Measure search time
  - [ ] Measure LLM analysis time

**Questions to Answer:**
- Is knowledge graph useful or over-engineered?
- Does context injection improve Claude's responses?
- Are there performance bottlenecks?

---

### Day 13-14: Polish & Documentation

- [ ] **Review all error messages**
  - [ ] Intentionally cause errors
  - [ ] Check if messages are clear
  - [ ] Verify suggested fixes

- [ ] **Test help text**
  - [ ] Run `cortex help`
  - [ ] Verify all commands listed
  - [ ] Check examples are correct

- [ ] **Update documentation**
  - [ ] Note confusing parts
  - [ ] Add missing examples
  - [ ] Update README with learnings

- [ ] **Bug triage**
  - [ ] List all bugs found
  - [ ] Categorize: critical/important/minor
  - [ ] Fix critical bugs
  - [ ] Create issues for others

---

## 🎯 Success Criteria

By the end of 14 days, you should be able to answer YES to:

### Functionality
- [ ] Events captured reliably (>99% success rate)
- [ ] Search finds relevant results (>80% accuracy)
- [ ] Insights are high quality (>70% useful)
- [ ] Deduplication works (no spam)
- [ ] Filtering works (no noise from binary files)
- [ ] Context injection helps (not annoying)

### Reliability
- [ ] No data loss (even with crashes)
- [ ] Graceful degradation (works without Ollama)
- [ ] No memory leaks (daemon stable >24 hours)
- [ ] Queue doesn't overflow (handles 100+ events/min)

### Usability
- [ ] Install "just works" on fresh machine
- [ ] Error messages are clear
- [ ] Commands are intuitive
- [ ] Documentation is accurate

### Performance
- [ ] Capture < 10ms
- [ ] Search < 500ms
- [ ] Daemon doesn't slow down development
- [ ] LLM analysis doesn't block workflow

---

## 📝 Daily Log Template

**Use this to track your experience each day:**

```markdown
## Day X - [Date]

### What I Did:
- [Task 1]
- [Task 2]

### What Worked:
- [Good thing 1]
- [Good thing 2]

### What Broke:
- [Bug 1] - Severity: Critical/Important/Minor
- [Bug 2] - Severity: Critical/Important/Minor

### UX Issues:
- [Confusing part 1]
- [Annoying thing 1]

### Performance Notes:
- Capture time: Xms
- Search time: Xms
- Insight quality: Good/OK/Poor

### Questions/Concerns:
- [Question 1]
- [Concern 1]

### Tomorrow's Focus:
- [Task for tomorrow]
```

---

## 🐛 Bug Tracking

### Critical Bugs (Must Fix Before Launch)
- [ ] [Bug description] - Found: Day X

### Important Bugs (Should Fix)
- [ ] [Bug description] - Found: Day X

### Minor Bugs (Nice to Fix)
- [ ] [Bug description] - Found: Day X

---

## 💭 Final Questions to Answer

After 14 days, honestly answer these:

1. **Would you recommend this to a friend?** Yes/No - Why?

2. **Did it actually save you time?** Yes/No - Examples?

3. **What's the #1 thing that should change?**

4. **What's the #1 thing that should NOT change?**

5. **Would you pay for this?** Yes/No - How much?

6. **What's missing that would make it 10x better?**

7. **Did context injection help Claude?** Yes/No/Sometimes - Examples?

8. **Is deduplication timing (30s) correct?** Yes/Too Short/Too Long

9. **Are insights useful or noise?** Useful/Mixed/Noise - Ratio?

10. **Ready to share publicly?** Yes/No - Why?

---

## ✅ Launch Readiness Checklist

Only proceed to public launch if you can check ALL of these:

- [ ] I used Cortex daily for 2+ weeks
- [ ] No critical bugs remain
- [ ] All claims in README are verified (performance, features)
- [ ] Install tested on fresh machine
- [ ] Error messages are clear and helpful
- [ ] Documentation is accurate and complete
- [ ] I would enthusiastically recommend it to others
- [ ] CI/CD is working
- [ ] Basic unit tests pass
- [ ] I'm proud of the code quality

**If any box is unchecked, don't launch yet!**

---

## 📊 Metrics Summary

Track these throughout testing:

| Metric | Target | Actual | Pass? |
|--------|--------|--------|-------|
| Capture time | <10ms | ___ ms | ☐ |
| Search time | <500ms | ___ ms | ☐ |
| Insight quality | >70% useful | ___% | ☐ |
| Uptime | >99% | ___% | ☐ |
| Data loss | 0 events | ___ events | ☐ |
| Install success | 100% | ___% | ☐ |

---

**Remember**: It's better to delay launch and fix issues than to release something you're not proud of. Take your time, be thorough, and trust the process.

Good luck! 🚀
