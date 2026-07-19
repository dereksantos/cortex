> **SHIPPED.** `web_search` landed lean as `internal/tools/web_search.go`,
> not the full MECE plan below. This doc is the pre-build plan, kept for
> history — check the code for what actually exists.

# Enhanced Web Search Tool Plan with MECE Verification & Quality Assurance

## Overview
Add a `web_search` tool to the loop system that performs read-only web searches and content extraction. This tool will be part of the `cmd/cortex/tools` package and follow the existing tool patterns, with comprehensive verification and quality assurance throughout implementation.

## Requirements

### Core Functionality
- **Read-only web access**: Only fetch content, no form submissions or authentication
- **Safe HTTP operations**: Respect robots.txt, rate limiting, and error handling
- **Content extraction**: Extract and summarize text content from HTML pages
- **Structured output**: Return search results with titles, URLs, and summaries
- **Error handling**: Graceful degradation on network failures or malformed content

### Integration Points
- **Tool system**: Register as a `ToolCall` with `Execute` method
- **ToolDeps**: Use existing `ToolDeps` for dependencies
- **Output management**: Follow existing patterns for large content (study/spill)
- **Error reporting**: Return errors as observations, not harness failures
- **Security**: Risk-gated like other tools

### Technical Specifications
- **Tool name**: `web_search`
- **Parameters**: 
  - `query` (required): Search query string
  - `max_results` (optional): Number of results to return (default: 5)
  - `timeout` (optional): Request timeout in seconds (default: 30)
- **Output format**: Structured search results with citations
- **Rate limiting**: Built-in delays to avoid overwhelming servers
- **User agent**: Identify as loop system for proper handling

## Enhanced MECE Implementation Plan with Verification

### Phase 1: Core Web Search Functionality
**Focus: Basic Google search and response with comprehensive verification**

#### 1A. HTTP Client Setup
**Verification Steps:**
- [ ] **Unit Test**: HTTP client configuration test
- [ ] **Integration Test**: Request with proper headers and timeouts
- [ ] **Usage Verification**: Test with various timeout values
- [ ] **Review Pass**: Security audit of HTTP client configuration

**Tests:**
```go
// TestHTTPClientConfiguration
func TestHTTPClientConfiguration(t *testing.T) {
    // Verify headers include user-agent
    // Verify timeout settings
    // Verify retry logic configuration
}

// TestRequestWithTimeouts
func TestRequestWithTimeouts(t *testing.T) {
    // Test short timeout handling
    // Test long timeout handling
    // Test timeout error propagation
}
```

#### 1B. Search API Integration
**Verification Steps:**
- [ ] **Unit Test**: API client initialization and configuration
- [ ] **Integration Test**: Mock API response handling
- [ ] **Usage Verification**: Test with different query types
- [ ] **Review Pass**: API key security validation

**Tests:**
```go
// TestSearchAPIConfiguration
func TestSearchAPIConfiguration(t *testing.T) {
    // Verify API endpoint configuration
    // Verify API key handling
    // Verify request parameters
}

// TestQueryExecution
func TestQueryExecution(t *testing.T) {
    // Test simple query
    // Test complex query with special characters
    // Test empty query handling
}
```

#### 1C. Response Validation
**Verification Steps:**
- [ ] **Unit Test**: Response structure validation
- [ ] **Integration Test**: Error handling for malformed responses
- [ ] **Usage Verification**: Test with various result counts
- [ ] **Review Pass**: Rate limit handling validation

**Tests:**
```go
// TestResponseStructure
func TestResponseStructure(t *testing.T) {
    // Verify required fields present
    // Verify data types
    // Verify field validation
}

// TestErrorHandling
func TestErrorHandling(t *testing.T) {
    // Test API error responses
    // Test network errors
    // Test malformed JSON handling
}
```

---

### Phase 2: Content Processing
**Focus: Fetch and process web page content with comprehensive verification**

#### 2A. HTML Fetching
**Verification Steps:**
- [ ] **Unit Test**: URL validation and normalization
- [ ] **Integration Test**: HTML fetching with security checks
- [ ] **Usage Verification**: Test with various URL formats
- [ ] **Review Pass**: Security vulnerability assessment

**Tests:**
```go
// TestURLValidation
func TestURLValidation(t *testing.T) {
    // Test valid URLs
    // Test invalid URLs
    // Test URL normalization
}

// TestHTMLFetching
func TestHTMLFetching(t *testing.T) {
    // Test successful fetch
    // Test network errors
    // Test security violations
}
```

#### 2B. HTML Parsing
**Verification Steps:**
- [ ] **Unit Test**: HTML parser functionality
- [ ] **Integration Test**: Content extraction from various HTML structures
- [ ] **Usage Verification**: Test with complex HTML pages
- [ ] **Review Pass**: Parsing accuracy validation

**Tests:**
```go
// TestHTMLParsing
func TestHTMLParsing(t *testing.T) {
    // Test simple HTML
    // Test complex HTML with nested elements
    // Test malformed HTML handling
}

// TestContentExtraction
func TestContentExtraction(t *testing.T) {
    // Test main content extraction
    // Test script/style removal
    // Test text cleaning
}
```

#### 2C. Content Summarization
**Verification Steps:**
- [ ] **Unit Test**: Summarization algorithm
- [ ] **Integration Test**: Large content handling
- [ ] **Usage Verification**: Test with various content lengths
- [ ] **Review Pass**: Summary quality assessment

**Tests:**
```go
// TestSummarization
func TestSummarization(t *testing.T) {
    // Test short content
    // Test long content
    // Test content with special characters
}

// TestContentTruncation
func TestContentTruncation(t *testing.T) {
    // Test truncation at limits
    // Test truncation markers
    // Test content preservation
}
```

---

### Phase 3: Tool Integration
**Focus: Integrate with loop's tool system with comprehensive verification**

#### 3A. Tool Registration
**Verification Steps:**
- [ ] **Unit Test**: Tool registration and parameter validation
- [ ] **Integration Test**: Tool execution in loop context
- [ ] **Usage Verification**: Test tool with various inputs
- [ ] **Review Pass**: Tool schema validation

**Tests:**
```go
// TestToolRegistration
func TestToolRegistration(t *testing.T) {
    // Test tool name registration
    // Test parameter validation
    // Test tool metadata
}

// TestToolExecution
func TestToolExecution(t *testing.T) {
    // Test successful execution
    // Test parameter errors
    // Test execution context
}
```

#### 3B. Output Formatting
**Verification Steps:**
- [ ] **Unit Test**: Result formatting and citation generation
- [ ] **Integration Test**: Output compatibility with loop UI
- [ ] **Usage Verification**: Test with various result types
- [ ] **Review Pass**: Output consistency validation

**Tests:**
```go
// TestResultFormatting
func TestResultFormatting(t *testing.T) {
    // Test result structure
    // Test citation generation
    // Test ranking and filtering
}

// TestOutputCompatibility
func TestOutputCompatibility(t *testing.T) {
    // Test output format
    // Test UI integration
    // Test error output
}
```

#### 3C. Security & Rate Limiting
**Verification Steps:**
- [ ] **Unit Test**: Security controls implementation
- [ ] **Integration Test**: Rate limiting effectiveness
- [ ] **Usage Verification**: Test security boundaries
- [ ] **Review Pass**: Security audit completion

**Tests:**
```go
// TestSecurityControls
func TestSecurityControls(t *testing.T) {
    // Test read-only access
    // Test URL validation
    // Test input sanitization
}

// TestRateLimiting
func TestRateLimiting(t *testing.T) {
    // Test per-domain limits
    // Test global limits
    // Test backoff logic
}
```

---

### Phase 4: Study Integration
**Focus: Connect with study philosophy with comprehensive verification**

> **Boundary (2026-06-27):** `web_search` is a **main-loop tool only**. Do NOT add
> it to the study subagent's tool set — per [`study-subagent.md`](../study-subagent.md)
> §1, `Study.Tools` is a closed `{outline, grep, read_file}` and "no `search` tool"
> is a verified invariant (`verify-study.sh`). "Study integration" here means web
> results flow into memory/findings the coder can later recall, never that the
> bounded study researcher gains web access.

#### 4A. Findings Prefix Creation
**Verification Steps:**
- [ ] **Unit Test**: Findings prefix generation
- [ ] **Integration Test**: Study integration with findings
- [ ] **Usage Verification**: Test with various search results
- [ ] **Review Pass**: Findings quality assessment

**Tests:**
```go
// TestFindingsPrefixGeneration
func TestFindingsPrefixGeneration(t *testing.T) {
    // Test simple findings
    // Test complex findings
    // Test findings caching
}

// TestStudyIntegration
func TestStudyIntegration(t *testing.T) {
    // Test study tool integration
    // Test findings storage
    // Test session recall
}
```

#### 4B. Index Building
**Verification Steps:**
- [ ] **Unit Test**: Index creation and management
- [ ] **Integration Test**: Index search functionality
- [ ] **Usage Verification**: Test index performance
- [ ] **Review Pass**: Index accuracy validation

**Tests:**
```go
// TestIndexCreation
func TestIndexCreation(t *testing.T) {
    // Test index structure
    // Test content indexing
    // Test metadata storage
}

// TestIndexSearch
func TestIndexSearch(t *testing.T) {
    // Test search functionality
    // Test result ranking
    // Test search accuracy
}
```

#### 4C. Study-Ready Content
**Verification Steps:**
- [ ] **Unit Test**: Study content preparation
- [ ] **Integration Test**: Study tool compatibility
- [ ] **Usage Verification**: Test study workflows
- [ ] **Review Pass**: Study integration validation

**Tests:**
```go
// TestStudyContentPreparation
func TestStudyContentPreparation(t *testing.T) {
    // Test content formatting
    // Test citation generation
    // Test study window sizing
}

// TestStudyToolCompatibility
func TestStudyToolCompatibility(t *testing.T) {
    // Test study tool integration
    // Test content compatibility
    // Test study workflow
}
```

---

### Phase 5: Testing & Validation
**Focus: Ensure reliability and correctness with comprehensive verification**

#### 5A. Unit Tests
**Verification Steps:**
- [ ] **Code Coverage**: Achieve 90%+ test coverage
- [ ] **Test Quality**: Review test effectiveness
- [ ] **Performance**: Test test execution speed
- [ ] **Review Pass**: Test suite validation

**Tests:**
```go
// Comprehensive unit test suite
// Test all core functionality
// Test edge cases
// Test error conditions
```

#### 5B. Integration Tests
**Verification Steps:**
- [ ] **End-to-End Testing**: Test complete workflows
- [ ] **Mock Testing**: Test with mocked dependencies
- [ ] **Environment Testing**: Test in various environments
- [ ] **Review Pass**: Integration test validation

**Tests:**
```go
// Integration test suite
// Test with mocked Google API
// Test with real database
// Test in production-like environment
```

#### 5C. Performance Tests
**Verification Steps:**
- [ ] **Load Testing**: Test with high load
- [ ] **Stress Testing**: Test under extreme conditions
- [ ] **Resource Testing**: Test memory and CPU usage
- [ ] **Review Pass**: Performance validation

**Tests:**
```go
// Performance test suite
// Test response times
// Test memory usage
// Test concurrent requests
```

---

### Phase 6: Documentation & Maintenance
**Focus: Support and sustain the tool with comprehensive verification**

#### 6A. Technical Documentation
**Verification Steps:**
- [ ] **Documentation Completeness**: Review documentation coverage
- [ ] **Code Documentation**: Review inline documentation
- [ ] **API Documentation**: Review API docs
- [ ] **Review Pass**: Documentation validation

#### 6B. User Documentation
**Verification Steps:**
- [ ] **User Guide Testing**: Test user workflows
- [ ] **Example Testing**: Test provided examples
- [ ] **Tutorial Testing**: Test tutorials
- [ ] **Review Pass**: User documentation validation

#### 6C. Monitoring & Maintenance
**Verification Steps:**
- [ ] **Monitoring Setup**: Verify monitoring implementation
- [ ] **Alert Testing**: Test alert functionality
- [ ] **Maintenance Procedures**: Review maintenance docs
- [ ] **Review Pass**: Monitoring validation

---

## MECE Implementation Timeline with Verification

### **Week 1-2: Core Web Search (Phase 1)**
- **Checkpoint 1**: Basic Google search and response
- **Verification**: All 1A, 1B, 1C verification steps complete
- **Tests**: Unit tests for HTTP client, API integration, response validation
- **Usage Verification**: Manual testing with various queries
- **Review Pass**: Security and functionality audit

### **Week 3-4: Content Processing (Phase 2)**
- **Checkpoint 2**: Can fetch and extract content from web pages
- **Verification**: All 2A, 2B, 2C verification steps complete
- **Tests**: Unit tests for HTML fetching, parsing, summarization
- **Usage Verification**: Manual testing with various URLs
- **Review Pass**: Parsing accuracy and security audit

### **Week 5-6: Tool Integration (Phase 3)**
- **Checkpoint 3**: Tool works within loop system
- **Verification**: All 3A, 3B, 3C verification steps complete
- **Tests**: Integration tests for tool registration, formatting, security
- **Usage Verification**: Test tool in loop environment
- **Review Pass**: Tool system compatibility audit

### **Week 7-8: Study Integration (Phase 4)**
- **Checkpoint 4**: Tool integrates with study philosophy
- **Verification**: All 4A, 4B, 4C verification steps complete
- **Tests**: Integration tests for findings, index, study content
- **Usage Verification**: Test study workflows
- **Review Pass**: Study integration validation

### **Week 9-10: Testing & Validation (Phase 5)**
- **Checkpoint 5**: Comprehensive testing complete
- **Verification**: All 5A, 5B, 5C verification steps complete
- **Tests**: Full test suite execution and validation
- **Usage Verification**: Production-like testing
- **Review Pass**: Test suite validation

### **Week 11-12: Documentation & Maintenance (Phase 6)**
- **Checkpoint 6**: Tool production-ready
- **Verification**: All 6A, 6B, 6C verification steps complete
- **Tests**: Documentation and maintenance procedure testing
- **Usage Verification**: User acceptance testing
- **Review Pass**: Final quality audit

---

## Quality Assurance Framework

### **Quality Gates**
1. **Security Gate**: All security verification steps complete
2. **Functionality Gate**: All core functionality verified
3. **Performance Gate**: All performance tests pass
4. **Integration Gate**: All integration tests pass
5. **Documentation Gate**: All documentation complete
6. **User Acceptance Gate**: User testing successful

### **Review Process**
1. **Code Review**: Technical correctness and best practices
2. **Security Review**: Vulnerability assessment and mitigation
3. **Performance Review**: Optimization and scalability analysis
4. **Documentation Review**: Completeness and clarity assessment
5. **User Review**: Usability and workflow validation

---

## Technical Architecture

### Tool Structure
```
web_search (ToolCall)
├── Execute(ctx, deps) (string, error)
├── validateParams()
├── performSearch()
├── extractContent()
├── formatResults()
└── handleErrors()
```

### Dependencies
- **HTTP client**: Standard library with custom transport
- **HTML parser**: Standard library (golang.org/x/net/html)
- **URL handling**: Standard library (net/url)
- **Timeouts**: Context-aware timeouts
- **Rate limiting**: Built-in delays and request tracking

### Security Considerations
- **Read-only operations**: No form submissions or authentication
- **Input validation**: Sanitize search queries
- **Output validation**: Ensure URLs are safe to visit
- **Rate limiting**: Prevent abuse and server overload
- **Error isolation**: Network failures don't crash the system

---

## Success Criteria

1. **Functional**: Tool performs searches and extracts content reliably
2. **Performance**: Response times under 5 seconds for typical queries
3. **Reliability**: 99.9% uptime with graceful error handling
4. **Security**: No security vulnerabilities or data leaks
5. **Integration**: Seamlessly works with existing loop tools
6. **User experience**: Clear output with proper citations and formatting

---

## Dependencies & External Services

### Search APIs (choose one)
- **Google Custom Search API**: High quality but requires API key
- **DuckDuckGo HTML scraping**: Free, no API key required
- **Brave Search API**: Good balance of features and ease of use

### HTML Parsing
- **Standard library**: `golang.org/x/net/html`
- **Alternative**: Third-party parser if standard library insufficient

### Rate Limiting
- **Built-in delays**: Between requests to same domain
- **Request tracking**: Per-domain and global limits
- **Backoff logic**: For failed requests

---

## Code Structure

### File Organization
```
cmd/cortex/tools/
├── web_search.go          # Main tool implementation
├── web_search_test.go     # Unit tests
└── web_search_integration_test.go  # Integration tests
```

### Implementation Details
- **Tool registration**: In `tools.go` with other tools
- **Error handling**: Consistent with existing tools
- **Output formatting**: Follow existing patterns
- **Documentation**: Add to tool help and README

---

## Risk Assessment

### High Risk
- **API dependency**: Search API changes or rate limits
- **HTML parsing complexity**: Dynamic content or anti-bot measures
- **Legal compliance**: Terms of service violations

### Medium Risk
- **Performance**: Slow response times with large pages
- **Memory usage**: Large page content handling
- **Error recovery**: Graceful degradation on failures

### Low Risk
- **Integration**: Tool system compatibility
- **Testing**: Comprehensive test coverage
- **Documentation**: Clear user instructions

---

## Timeline

### Week 1-2: Core Implementation
- Basic tool structure
- HTTP client and parameter validation
- Unit tests

### Week 3-4: Content Extraction
- HTML parsing and content extraction
- Result formatting and citations
- Integration tests

### Week 5-6: Search Integration
- Search API integration
- Rate limiting and error handling
- Performance optimization

### Week 7-8: Testing & Deployment
- Comprehensive testing
- Documentation and examples
- Production rollout

---

## Next Steps

1. **Choose search API** and set up credentials
2. **Implement core tool structure** following existing patterns
3. **Write tests** before implementing features
4. **Add to tool configuration** and documentation
5. **Monitor performance** and iterate based on feedback

This enhanced MECE plan ensures each phase has comprehensive verification, testing, usage validation, and review passes to guarantee quality and correctness throughout the implementation. Each checkpoint builds on the previous one, creating a robust, maintainable web search tool that integrates seamlessly with the loop system and study philosophy.