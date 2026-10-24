# CLI Command Classification Feature

## Overview

This feature introduces intelligent user message classification to automatically determine whether input messages contain plain CLI commands that can be executed immediately versus complex requests requiring Cortex assistance. The system analyzes message size, content complexity, and risk profiles to make real-time execution decisions.

## System Architecture

### High-Level Flow

```
┌─────────────────────────────────────────────────────────────────┐
│                    USER INPUT PROCESSING                         │
├─────────────────────────────────────────────────────────────────┤
│ 1. Size Analysis → Quick Reject                                  │
│    ┌───────────────────────┐                                    │
│    │  isSmallForExecution  │                                    │
│    │  (character/token)   │                                    │
│    └───────────────────────┘                                    │
│                                                                 │
│ 2. CLI Classification → Type Detection                          │
│    ┌───────────────────────┐                                    │
│    │  classifyAsCLI()     │                                    │
│    │  (pattern + risk)    │                                    │
│    └───────────────────────┘                                    │
│                                                                 │
│ 3. Risk Assessment → Safety Gate                               │
│    ┌───────────────────────┐                                    │
│    │  riskJudge()          │                                    │
│    │  (existing shellrisk)│                                    │
│    └───────────────────────┘                                    │
│                                                                 │
│ 4. Decision Routing → Execute or Assist                         │
│    ┌───────────────────────┐                                    │
│    │  routeByClassification │                                    │
│    │  (auto-execute vs help)│                                    │
│    └───────────────────────┘                                    │
└─────────────────────────────────────────────────────────────────┘
```

### Component Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│                    IMPLEMENTATION LAYERS                        │
├─────────────────────────────────────────────────────────────────┤
│                                                              │
│  ┌─────────────────────┐  ┌─────────────────────┐            │
│  │   Message Size      │  │   CLI Classifier   │            │
│  │   Analysis          │  │   Engine           │            │
│  │   (isSmallForExec)  │  │   (pattern + risk)  │            │
│  └─────────────────────┘  └─────────────────────┘            │
│                                                              │
│  ┌─────────────────────┐  ┌─────────────────────┐            │
│  │   Decision Router   │  │   Command Executor  │            │
│  │   (routeByClass)    │  │   (auto-execute)    │            │
│  └─────────────────────┘  └─────────────────────┘            │
│                                                              │
│  ┌─────────────────────┐  ┌─────────────────────┐            │
│  │   Turn Integration  │  │   Risk Integration  │            │
│  │   (turn.go)        │  │   (shellrisk.go)    │            │
│  └─────────────────────┘  └─────────────────────┘            │
│                                                              │
│  ┌─────────────────────┐  ┌─────────────────────┐            │
│  │   Configuration     │  │   Monitoring       │            │
│  │   Manager           │  │   & Logging        │            │
│  └─────────────────────┘  └─────────────────────┘            │
│                                                              │
└─────────────────────────────────────────────────────────────────┘
```

## Epic Breakdown

### Epic 1: Core Infrastructure
**Goal**: Establish the foundational components for CLI classification
**Duration**: 2 weeks
**Team Size**: 2-3 engineers

#### Tasks:
1. **Message Size Analysis** (Week 1)
   - Implement `isSmallForExecution()` function
   - Define size thresholds (characters, tokens)
   - Add configuration management
   - Write unit tests

2. **CLI Classification Engine** (Week 1)
   - Implement `classifyAsCLI()` function
   - Create pattern matching for command detection
   - Define CLI type classifications
   - Add risk assessment integration

3. **Decision Router** (Week 1)
   - Implement routing logic
   - Create execution path selection
   - Add decision logging
   - Write integration tests

4. **Configuration System** (Week 2)
   - Add feature flags
   - Configure thresholds and rules
   - Implement validation
   - Add environment variable support

### Epic 2: Classification Engine Enhancement
**Goal**: Improve classification accuracy and sophistication
**Duration**: 3 weeks
**Team Size**: 2 engineers

#### Tasks:
1. **Advanced Pattern Matching** (Week 2)
   - Add command vocabulary database
   - Implement shell command detection
   - Add script vs single command classification
   - Create complexity scoring

2. **Risk Integration** (Week 2)
   - Integrate with existing shellrisk system
   - Add risk-based classification overrides
   - Implement security exception handling
   - Add audit logging

3. **Performance Optimization** (Week 2)
   - Implement caching strategies
   - Optimize classification algorithms
   - Add parallel processing
   - Create performance monitoring

4. **Edge Case Handling** (Week 3)
   - Add ambiguous input handling
   - Implement fallback mechanisms
   - Create error recovery
   - Add user feedback loops

### Epic 3: Integration and Deployment
**Goal**: Integrate with existing Cortex systems and deploy
**Duration**: 2 weeks
**Team Size**: 2-3 engineers

#### Tasks:
1. **Turn Processing Integration** (Week 3)
   - Modify `turnWithImages()` to use classifier
   - Add auto-execution pipeline
   - Integrate with existing tool execution
   - Add error handling

2. **Web UI Integration** (Week 3)
   - Add classification status indicators
   - Create user feedback mechanisms
   - Implement visibility controls
   - Add user preference management

3. **Command Execution Bridge** (Week 3)
   - Create command execution interface
   - Implement bash execution with logging
   - Add result handling and error propagation
   - Create audit trail

4. **Testing and Validation** (Week 4)
   - Create comprehensive test suite
   - Add integration test scenarios
   - Implement performance benchmarks
   - Add user acceptance testing

## Verification Tools and Testing Strategy

### Unit Test Framework

```go
// Test files structure:
// test/
//   ├── cli_classifier_test.go
//   ├── message_size_test.go
//   ├── decision_router_test.go
//   ├── risk_integration_test.go
//   ├── turn_integration_test.go
//   └── performance_test.go
```

#### Test Categories:

1. **Size Analysis Tests**
   - Character count thresholds
   - Token estimation accuracy
   - Edge case handling

2. **CLI Classification Tests**
   - Pattern matching accuracy
   - Command type identification
   - Risk assessment integration
   - Confidence scoring

3. **Decision Router Tests**
   - Route selection logic
   - Auto-execution triggers
   - Help request routing
   - Error handling

4. **Integration Tests**
   - End-to-end scenarios
   - Turn processing integration
   - Security boundary testing
   - Performance regression

5. **Load and Stress Tests**
   - Concurrent classification
   - High-volume processing
   - Memory usage patterns
   - Response time requirements

### Automated Testing Pipeline

```yaml
# .github/workflows/cli-classification.yml
test:
  stages:
    - unit
    - integration
    - performance
    - security
    
  unit_tests:
    script:
      - go test ./test/cli_classifier_test.go -v
      - go test ./test/message_size_test.go -v
      - go test ./test/decision_router_test.go -v
      
  integration_tests:
    script:
      - go test ./test/turn_integration_test.go -v -tags=integration
      - go test ./test/edge_cases_test.go -v
      
  performance_tests:
    script:
      - go test ./test/performance_test.go -v -bench
      - go test ./test/load_test.go -v -bench
      
  security_tests:
    script:
      - go test ./test/security_test.go -v
      - go test ./test/audit_test.go -v
```

### Continuous Integration Metrics

#### Quality Gates:

1. **Test Coverage**: Minimum 90% line coverage
2. **Performance**: Classification < 50ms, execution < 200ms
3. **Security**: Zero false positives, zero false negatives
4. **Reliability**: 99.9% uptime, automatic rollback on failures

#### Monitoring Metrics:

```go
// Metrics collection
type ClassificationMetrics struct {
    TotalMessages      int64
    AutoExecutedCount  int64
    HelpRequestedCount int64
    ClassificationErrors int64
    AverageLatency     time.Duration
    CacheHitRate       float64
    ErrorRate          float64
}

// Performance indicators
type PerformanceIndicators struct {
    ClassificationTimeP95 time.Duration
    ClassificationTimeP99 time.Duration
    MemoryUsageMB        float64
    GoroutineCount        int
    ThroughputMessagesPS  float64
}
```

### Validation Tools

#### 1. Classification Accuracy Validator
```bash
# Test classification accuracy
./scripts/validate_classification.sh \
  --test-data test/fixtures/classification/ \
  --model production \
  --threshold 0.95
```

#### 2. Security Compliance Checker
```bash
# Security validation
./scripts/security_validate.sh \
  --blocklist security/blocklist.txt \
  --allowlist security/allowlist.txt \
  --audit-log security/audit.log
```

#### 3. Performance Regression Detector
```bash
# Performance monitoring
./scripts/performance_monitor.sh \
  --baseline baseline_metrics.json \
  --current current_metrics.json \
  --threshold 10% \
  --alert email:team@company.com
```

## User Input Scoring Guide

### Input Characteristics Assessment

#### Message Size (0-100 points):
- < 50 chars: 100 points (auto-execute)
- 50-100 chars: 75 points (auto-execute)
- 100-200 chars: 50 points (assist)
- 200-500 chars: 25 points (assist)
- > 500 chars: 0 points (always assist)

#### Command Pattern Detection (0-100 points):
- Clear single command: 100 points
- Script/multiline: 75 points
- Ambiguous/unclear: 25 points
- Non-command: 0 points

#### Risk Level (0-100 points):
- Safe path (ls, cat, grep): 100 points
- Low risk (git status, go build): 75 points
- Medium risk (git commit, docker): 50 points
- High risk (rm, sudo): 25 points
- Catastrophic risk (rm -rf, sudo): 0 points

#### Overall Classification Score:
```
Score = (Size × 0.3) + (Pattern × 0.4) + (Risk × 0.3)
```

#### Decision Rules:
- Score ≥ 80: Auto-execute
- Score ≥ 60: Assist with confirmation
- Score < 60: Always assist

### Example Scoring:

| User Input | Size | Pattern | Risk | Score | Decision |
|------------|------|---------|------|-------|----------|
| `ls -la` | 100 | 100 | 100 | 100 | Auto-execute |
| `git status` | 75 | 75 | 75 | 78 | Auto-execute |
| `fix the bug in auth.go` | 25 | 25 | 25 | 25 | Assist |
| `rm -rf /tmp/test` | 100 | 100 | 0 | 30 | Assist |
| `build the project and run tests` | 50 | 25 | 50 | 47 | Assist |
| `docker run -it ubuntu bash` | 75 | 75 | 25 | 65 | Assist with confirmation |

## Implementation Checklist

### Phase 1: Core Infrastructure ✅
- [ ] Message size analysis
- [ ] CLI classification engine
- [ ] Decision router
- [ ] Configuration system
- [ ] Basic unit tests

### Phase 2: Enhancement ✅
- [ ] Advanced pattern matching
- [ ] Risk integration
- [ ] Performance optimization
- [ ] Edge case handling
- [ ] Integration testing

### Phase 3: Deployment ✅
- [ ] Turn processing integration
- [ ] Web UI integration
- [ ] Command execution bridge
- [ ] Production testing
- [ ] Monitoring setup

## Risk Mitigation

### High Priority Risks:
1. **False Positives**: Commands executed without user intent
   - Mitigation: Conservative scoring, user confirmation required for risky commands

2. **Classification Errors**: Incorrect command identification
   - Mitigation: Fallback to assistance on errors, continuous improvement

3. **Security Vulnerabilities**: Command injection
   - Mitigation: Strict input validation, integration with existing security gates

### Medium Priority Risks:
4. **Performance Degradation**: Slow classification
   - Mitigation: Caching, optimization, performance monitoring

5. **User Experience**: Confusing behavior
   - Mitigation: Clear feedback, user preferences, opt-out mechanisms

### Low Priority Risks:
6. **Feature Drift**: Scope creep
   - Mitigation: Strict requirements, regular reviews

## Success Metrics

### Technical Metrics:
- Classification accuracy: 95%+
- Auto-execution success rate: 90%+
- Error rate: < 0.1%
- Response time: < 50ms (classification)

### User Metrics:
- User satisfaction: 4.5/5+
- Time to resolution: 30% improvement
- Support tickets: 40% reduction
- Task completion rate: 90%+

### Business Metrics:
- Operational cost reduction: 30%+
- Productivity improvement: 25%+
- System reliability: 99.9%+
- Security compliance: 100%

## Conclusion

The CLI Command Classification feature represents a significant enhancement to Cortex's user interaction capabilities. By automatically identifying and executing plain CLI commands, we can dramatically improve user productivity while maintaining security and reliability.

The phased approach ensures incremental delivery, with each phase building on the previous one. Comprehensive testing and monitoring guarantee that the system meets all quality and performance requirements.

This feature aligns perfectly with Cortex's mission to provide intelligent, efficient, and secure command-line interaction for developers and technical users.