# Cortex Evaluation Framework

## Problem Statement

First we must measure the problem. Then we must measure the solution.

**The Problem:**
Over time a project's artifacts grow beyond the limits that an LLM model can understand within one context window. This creates higher probability of inaccurate responses for future sessions. A human can steer the LLM however this often leads to repeating oneself across sessions. It is this steering that must be automatically captured and remembered. Otherwise humans must manually update persisted context.

**The Solution:**
- Agentic memory recall
- Agentic random reflection (artificial dreaming)
- Agentic real time reflection (artificial reflection)
- Curated context in git through human interaction

**Models:**
- Frontier model (GPT-4, Claude Opus, Sonnet)
- Local model (Ollama)

**Key Variables:**
- Let `x` represent a context chain much larger than the frontier model's limit to create the necessity for effective memory management.
- Let `y` represent the context chain's artifacts (decisions and code)
- Let `p` represent an input prompt
- Let `m` represent the generated memory

**Evaluation Setup:**
- Pre-existing context chain `c`: Previous work session (e.g., implementing JWT auth)
- Artifact `a`: The resulting code/decisions from `c`
- Prompt `p`: New related question (e.g., "How should I handle password resets?")

**Agentic Processors:**
- Compare against established industry practices and idioms
- Consider agentically analyzed conversations for local project standards and goals
- Privately analyzed context from developers completely local to device

---

## Table of Contents

1. [Evaluation Overview](#evaluation-overview)
2. [Linear Evaluation (Single Path)](#linear-evaluation-single-path)
3. [Tree Evaluation (Divergent Patterns)](#tree-evaluation-divergent-patterns)
4. [Real-World Codebase Understanding](#real-world-codebase-understanding)
5. [Real-World Eval Metrics](#real-world-eval-metrics)
6. [Metrics & Scoring](#metrics--scoring)
7. [Test Data Creation](#test-data-creation)
8. [Implementation Architecture](#implementation-architecture)
9. [Statistical Analysis](#statistical-analysis)
10. [Roadmap](#roadmap)

---

## Evaluation Overview

### Core Research Question

**Does context injection from Cortex improve AI assistant output quality?**

**Hypothesis**: When Cortex captures context chain `c` and injects relevant context for prompt `p`, the AI response will be:
- More consistent with past decisions
- More correct given artifact `a`
- More complete (mentioning relevant facts)
- Less likely to hallucinate
- More valuable to the developer

### Experimental Design

**Comparison**:
- **Condition A (With Cortex)**: AI responds to `p` with Cortex-injected context from `c`
- **Condition B (Without Cortex)**: AI responds to `p` with no additional context (baseline)

**Measure**: Quality delta (A - B) across multiple dimensions

### Evaluation Types

1. **Linear Evaluation**: Single development path, measure consistency
2. **Tree Evaluation**: Multiple valid paths, measure path-specific consistency
3. **Temporal Evaluation**: Context evolution over time
4. **Counterfactual Evaluation**: Alternative decision paths

---

## Linear Evaluation (Single Path)

### Design

**Scenario Structure**:
```yaml
scenario:
  id: "linear-auth-001"
  type: "linear"

  # Step 1: Establish context
  context_chain:
    - event: Decision
      content: "Use JWT for stateless authentication"
      rationale: "Microservices architecture needs stateless auth"
      timestamp: T0

    - event: Implementation
      file: auth.go
      content: "func GenerateToken(userID string) (string, error) { ... }"
      timestamp: T1

    - event: Pattern
      content: "Tokens in Authorization header, 24h expiry"
      timestamp: T2

  # Step 2: Test artifact
  artifact:
    files: [auth.go, middleware.go, config.go]
    dependencies: ["github.com/golang-jwt/jwt/v5"]

  # Step 3: Test prompt
  test_prompt: "How should I implement password reset?"

  # Step 4: Ground truth
  ground_truth:
    must_include:
      - "JWT token for reset link"
      - "Short expiry (1 hour)"
      - "Follow existing auth pattern"
    must_exclude:
      - "Session-based"
      - "Store in database"
    consistency_requirements:
      - "Use same JWT library"
      - "Maintain stateless design"
```

### Metrics

**1. Consistency Score** (0-1)
```
Does response align with decisions in context chain?

Evaluation:
- Extract key decisions from context (e.g., "stateless auth")
- Check if response violates any decisions
- Score = 1 - (violations / total_decisions)
```

**2. Correctness Score** (0-1)
```
Does response match ground truth?

Evaluation:
- Check presence of must_include items
- Check absence of must_exclude items
- Score = (included + not_excluded) / total_requirements
```

**3. Completeness Score** (0-1)
```
Does response mention relevant context?

Evaluation:
- Identify relevant facts from context chain
- Count how many are mentioned in response
- Score = mentioned / total_relevant
```

**4. Hallucination Score** (0-1, lower is better)
```
Does response invent facts not in context?

Evaluation:
- Extract factual claims from response
- Check if each claim is grounded in context or artifact
- Score = hallucinated_facts / total_facts
```

**5. Code Compatibility Score** (0-1)
```
Would generated code work with artifact?

Evaluation:
- Parse suggested code
- Check imports match artifact
- Check API usage matches artifact
- Run type checker if possible
- Score = compatible_aspects / total_aspects
```

### Example Test Case

```go
func TestLinear_AuthPasswordReset(t *testing.T) {
    scenario := Scenario{
        ID: "linear-auth-001",
        ContextChain: []Event{
            {Type: "decision", Content: "Use JWT for auth", Rationale: "Stateless"},
            {Type: "code", File: "auth.go", Content: GenerateTokenCode},
        },
        TestPrompt: "How should I implement password reset?",
        GroundTruth: GroundTruth{
            MustInclude: []string{"JWT", "short expiry", "email"},
            MustExclude: []string{"session", "database storage"},
        },
    }

    // Condition A: With Cortex
    cortex := NewCortex()
    cortex.IngestContext(scenario.ContextChain)
    contextInjected := cortex.GetRelevantContext(scenario.TestPrompt)

    responseA := AI.Generate(scenario.TestPrompt, contextInjected)
    scoreA := EvaluateResponse(responseA, scenario)

    // Condition B: Without Cortex
    responseB := AI.Generate(scenario.TestPrompt, nil)
    scoreB := EvaluateResponse(responseB, scenario)

    // Assert improvement
    assert.Greater(t, scoreA.Consistency, scoreB.Consistency)
    assert.Greater(t, scoreA.Overall, scoreB.Overall)
}
```

---

## Tree Evaluation (Divergent Patterns)

### What Are Tree Evals?

**Concept**: Test Cortex's ability to handle scenarios where:
1. Multiple valid implementation paths exist from same starting point
2. Past decisions constrain future choices differently per path
3. Context branches into divergent approaches
4. Different paths require different context injection

**Why This Matters**: Real development has multiple valid solutions. Cortex must:
- Maintain consistency within each path
- Not contaminate paths with incompatible context
- Help navigate decision trees correctly
- Handle temporal evolution and migrations

### Tree Eval Type 1: Multi-Path Consistency

**Research Question**: Given the same starting point, can Cortex maintain consistency within each divergent path?

**Scenario Structure**:
```yaml
scenario:
  id: "tree-storage-001"
  type: "multi-path"

  # Common starting point
  initial_context:
    - event: Requirement
      content: "Need to store user preferences"

  # Path A: SQL Database
  path_a:
    name: "PostgreSQL Path"
    context_chain:
      - event: Decision
        content: "Use PostgreSQL for user preferences"
        rationale: "Need ACID guarantees, relational queries"
        timestamp: T0

      - event: Implementation
        file: storage.go
        content: "sql.Open('postgres', connString)"
        timestamp: T1

      - event: Pattern
        content: "Use sqlx for database access, migrations in db/migrations/"
        timestamp: T2

    test_prompts:
      - prompt: "How should I add a new user setting?"
        expected_context: ["PostgreSQL", "migrations", "sqlx"]
        must_not_mention: ["NoSQL", "JSON files", "MongoDB"]

      - prompt: "How do I handle concurrent updates?"
        expected_context: ["transactions", "SELECT FOR UPDATE", "PostgreSQL"]
        must_not_mention: ["eventual consistency", "CAS"]

  # Path B: NoSQL Database
  path_b:
    name: "MongoDB Path"
    context_chain:
      - event: Decision
        content: "Use MongoDB for user preferences"
        rationale: "Need flexible schema, document model fits preferences"
        timestamp: T0

      - event: Implementation
        file: storage.go
        content: "mongo.Connect(context.Background(), options)"
        timestamp: T1

      - event: Pattern
        content: "Use mongo-driver, one collection per user"
        timestamp: T2

    test_prompts:
      - prompt: "How should I add a new user setting?"
        expected_context: ["MongoDB", "document update", "mongo-driver"]
        must_not_mention: ["SQL", "migrations", "ALTER TABLE"]

      - prompt: "How do I handle concurrent updates?"
        expected_context: ["optimistic locking", "version field", "MongoDB"]
        must_not_mention: ["transactions", "SELECT FOR UPDATE"]

# Evaluation
evaluation:
  metric: "Path Consistency"

  test_procedure:
    1. Run Path A scenarios with Cortex trained on Path A context
    2. Run Path B scenarios with Cortex trained on Path B context
    3. Cross-contamination test:
       - Run Path A prompts with Path B context injected (should score low)
       - Run Path B prompts with Path A context injected (should score low)

  success_criteria:
    - Path A prompts + Path A context > baseline
    - Path B prompts + Path B context > baseline
    - Path A prompts + Path B context < baseline (contamination detected)
    - Path B prompts + Path A context < baseline (contamination detected)
```

**What This Tests**:
- ✅ Cortex doesn't mix incompatible contexts
- ✅ Cortex selects path-appropriate context
- ✅ AI maintains internal consistency within a path
- ✅ Context injection improves path-specific accuracy

**Example Implementation**:
```go
func TestTree_MultiPath_Storage(t *testing.T) {
    scenario := LoadTreeScenario("tree-storage-001")

    // Test Path A: PostgreSQL
    cortexA := NewCortex()
    cortexA.IngestContext(scenario.PathA.ContextChain)

    for _, prompt := range scenario.PathA.TestPrompts {
        context := cortexA.GetRelevantContext(prompt.Prompt)
        response := AI.Generate(prompt.Prompt, context)

        // Should mention PostgreSQL concepts
        for _, expected := range prompt.ExpectedContext {
            assert.Contains(t, response, expected)
        }

        // Should NOT mention MongoDB concepts
        for _, forbidden := range prompt.MustNotMention {
            assert.NotContains(t, response, forbidden)
        }
    }

    // Test Path B: MongoDB (separate Cortex instance)
    cortexB := NewCortex()
    cortexB.IngestContext(scenario.PathB.ContextChain)

    for _, prompt := range scenario.PathB.TestPrompts {
        context := cortexB.GetRelevantContext(prompt.Prompt)
        response := AI.Generate(prompt.Prompt, context)

        // Should mention MongoDB concepts
        for _, expected := range prompt.ExpectedContext {
            assert.Contains(t, response, expected)
        }

        // Should NOT mention PostgreSQL concepts
        for _, forbidden := range prompt.MustNotMention {
            assert.NotContains(t, response, forbidden)
        }
    }

    // Cross-contamination test
    contextA := cortexA.GetRelevantContext(scenario.PathB.TestPrompts[0].Prompt)
    responseWrong := AI.Generate(scenario.PathB.TestPrompts[0].Prompt, contextA)

    // Should produce worse response (wrong context)
    scoreCorrect := Evaluate(cortexB, scenario.PathB.TestPrompts[0])
    scoreWrong := EvaluateResponse(responseWrong, scenario.PathB.TestPrompts[0])

    assert.Greater(t, scoreCorrect, scoreWrong, "Wrong context should hurt quality")
}
```

### Tree Eval Type 2: Decision Tree Navigation

**Research Question**: Given a hierarchical decision tree, does Cortex help navigate to consistent leaf nodes?

**Scenario Structure**:
```yaml
scenario:
  id: "tree-nav-api-001"
  type: "decision-tree"

  decision_tree:
    root:
      decision: "Choose API architecture"
      options:
        - REST
        - GraphQL
        - gRPC

    branches:
      rest:
        decision: "Choose framework"
        options:
          - gin:
              pattern: "Handler functions, middleware chain"
              routing: "Router groups"
          - echo:
              pattern: "Context-based handlers"
              routing: "Route registration"

      graphql:
        decision: "Choose server library"
        options:
          - gqlgen:
              pattern: "Schema-first, code generation"
              resolvers: "Resolver functions per field"
          - graphql-go:
              pattern: "Code-first, type definitions"
              resolvers: "Resolver methods on types"

  # Test paths through tree
  test_paths:
    - path: [REST, gin]
      context_chain:
        - "Decided on REST for simplicity"
        - "Using Gin framework"
        - "Pattern: Handler functions with c *gin.Context"

      test_prompts:
        - prompt: "How do I add authentication?"
          expected: "Gin middleware: router.Use(AuthMiddleware())"
          forbidden: ["GraphQL context", "gRPC interceptor"]

        - prompt: "How do I validate request body?"
          expected: "ShouldBindJSON on gin.Context"
          forbidden: ["GraphQL input types", "protobuf validation"]

    - path: [GraphQL, gqlgen]
      context_chain:
        - "Decided on GraphQL for flexible queries"
        - "Using gqlgen (schema-first)"
        - "Pattern: Schema in .graphql files, generated resolvers"

      test_prompts:
        - prompt: "How do I add authentication?"
          expected: "Directive in schema, middleware in generated code"
          forbidden: ["Gin middleware", "gRPC interceptor"]

        - prompt: "How do I validate input?"
          expected: "Input types in schema, custom scalars"
          forbidden: ["ShouldBind", "protobuf"]

evaluation:
  metric: "Tree Navigation Accuracy"

  measure:
    - Given context from path [REST, gin], prompts should get gin-specific answers
    - Given context from path [GraphQL, gqlgen], prompts should get gqlgen-specific answers
    - Cross-path contamination should score low

  score:
    - Path adherence: Response matches correct path (0-1)
    - Depth consistency: Response respects all decisions in path
    - Leaf correctness: Response uses leaf-specific patterns
```

**What This Tests**:
- ✅ Cortex respects hierarchical decisions (if REST chosen, don't suggest GraphQL)
- ✅ Cortex navigates to correct leaf (if Gin chosen, use Gin patterns not Echo)
- ✅ Context injection guides through decision tree correctly
- ✅ Early decisions correctly constrain later choices

### Tree Eval Type 3: Temporal Divergence

**Research Question**: When context evolves or migrations happen, does Cortex handle temporal boundaries correctly?

**Scenario Structure**:
```yaml
scenario:
  id: "tree-temporal-migration-001"
  type: "temporal-divergence"

  # Timeline of context evolution
  timeline:
    # Phase 1: Initial implementation
    phase_1:
      time_range: [T0, T10]
      context:
        - decision: "Use REST API with Express"
        - implementation: "app.get('/users', handler)"
        - pattern: "Route handlers, middleware"

      test_prompts:
        - prompt: "How do I add a new endpoint?"
          at_time: T5
          expected: "app.post('/endpoint', middleware, handler)"

    # Phase 2: Migration decision
    phase_2:
      time_range: [T10, T15]
      context:
        - decision: "Migrate to GraphQL for better client flexibility"
        - migration_plan: "Dual mode: REST deprecated, GraphQL primary"
        - implementation_start: "Set up Apollo Server"

      test_prompts:
        - prompt: "How do I add a new endpoint?"
          at_time: T12
          expected: "Add to GraphQL schema, create resolver (REST deprecated)"

        - prompt: "Should I use REST or GraphQL?"
          at_time: T12
          expected: "GraphQL for new features, REST is being phased out"

    # Phase 3: Post-migration
    phase_3:
      time_range: [T15, T20]
      context:
        - completion: "REST fully removed"
        - new_pattern: "GraphQL only, schema-first"

      test_prompts:
        - prompt: "How do I add a new endpoint?"
          at_time: T18
          expected: "Add to GraphQL schema (pure GraphQL now)"
          must_not_mention: ["REST", "Express routes"]

evaluation:
  metric: "Temporal Context Awareness"

  test_procedure:
    # Test that context injection respects time boundaries
    1. At T5 (Phase 1): Should suggest REST
    2. At T12 (Phase 2): Should suggest GraphQL, note REST deprecation
    3. At T18 (Phase 3): Should suggest GraphQL only, no REST

    # Cross-temporal contamination
    4. Prompt at T18 should NOT get T5 context (outdated)
    5. Prompt at T5 should NOT get T18 context (anachronistic)

  success_criteria:
    - Responses respect temporal boundaries
    - Recent context weighted higher than old context
    - Migration transitions handled smoothly
    - No anachronistic suggestions
```

**What This Tests**:
- ✅ Cortex handles evolving architectures
- ✅ Old context doesn't contaminate new decisions
- ✅ Temporal recency is considered
- ✅ Migration phases are respected
- ✅ Deprecated patterns are not suggested

### Tree Eval Type 4: Counterfactual Reasoning

**Research Question**: Can Cortex help explore "what if we had chosen differently?"

**Scenario Structure**:
```yaml
scenario:
  id: "tree-counterfactual-db-001"
  type: "counterfactual"

  # Actual path taken
  actual_path:
    context_chain:
      - decision: "Use MySQL for main database"
      - rationale: "Team familiar with SQL, ACID needed"
      - implementation: "database/sql with mysql driver"
      - challenges:
          - "Sharding is complex"
          - "Vertical scaling limits"

  # Counterfactual: What if we chose differently?
  counterfactual_paths:
    - name: "What if we chose PostgreSQL?"
      modified_decision: "Use PostgreSQL instead of MySQL"
      context_chain:
        - decision: "Use PostgreSQL for main database"
        - rationale: "Better JSON support, advanced features"
        - implementation: "database/sql with postgres driver"
        - advantages:
            - "Better JSON/JSONB support"
            - "Advanced indexing"
            - "Better concurrency"

      test_prompts:
        - prompt: "How would we handle JSON storage?"
          expected: "Use JSONB columns with GIN indexes"
          compare_to_actual: "MySQL uses JSON type with limited indexing"

    - name: "What if we chose MongoDB?"
      modified_decision: "Use MongoDB instead of MySQL"
      context_chain:
        - decision: "Use MongoDB for main database"
        - rationale: "Flexible schema, horizontal scaling"
        - implementation: "mongo-driver"
        - advantages:
            - "Easy sharding"
            - "Flexible schema"
            - "Horizontal scaling"

      test_prompts:
        - prompt: "How would we handle sharding?"
          expected: "MongoDB has built-in sharding with shard keys"
          compare_to_actual: "MySQL sharding requires Vitess or manual"

evaluation:
  metric: "Counterfactual Exploration Quality"

  test_procedure:
    1. Train Cortex on actual path (MySQL)
    2. Create separate Cortex instances for each counterfactual
    3. Compare responses to same prompts across paths
    4. Measure quality of alternatives suggested

  insights:
    - Can Cortex help understand trade-offs?
    - Does context injection show implications of different choices?
    - Can users explore alternatives post-facto?
```

**What This Tests**:
- ✅ Cortex can maintain parallel context timelines
- ✅ Enables exploration of alternative architectures
- ✅ Helps understand trade-offs of decisions
- ✅ Supports learning from paths not taken

### Tree Eval Type 5: Constraint Propagation

**Research Question**: When early decisions constrain later choices, does Cortex enforce those constraints?

**Scenario Structure**:
```yaml
scenario:
  id: "tree-constraints-001"
  type: "constraint-propagation"

  # Root constraint
  root_decision:
    content: "Target: Single-page application with no server-side rendering"
    constraints:
      - "All rendering happens client-side"
      - "API provides JSON only"
      - "SEO is not a priority"

  # Subsequent decisions that must respect root constraint
  decision_chain:
    - decision: "Choose React for frontend"
      valid: true  # Compatible with SPA constraint

    - decision: "Use Next.js"
      valid: false  # Violates constraint (Next.js is SSR-focused)
      violation: "Next.js contradicts 'no SSR' constraint"

    - decision: "Use client-side routing"
      valid: true  # Compatible with SPA constraint

    - decision: "Implement server-side pagination"
      valid: false  # Violates constraint (should be client-side)
      violation: "Server shouldn't render, only provide data"

  test_prompts:
    - prompt: "How should I implement routing?"
      expected: "Client-side routing (react-router)"
      forbidden: ["Next.js routing", "server-side routing"]
      constraint_check: "Must respect SPA constraint"

    - prompt: "Should I use Next.js?"
      expected: "No, violates SPA constraint. Use create-react-app or Vite"
      forbidden: ["Yes", "Next.js is good for SPAs"]
      constraint_check: "Must detect constraint violation"

    - prompt: "How do I implement pagination?"
      expected: "Client-side pagination with JSON API"
      forbidden: ["server-side rendering", "SSR pagination"]
      constraint_check: "Must respect data-only API constraint"

evaluation:
  metric: "Constraint Adherence"

  test_procedure:
    1. Inject root constraint context
    2. Test if AI suggestions respect constraints
    3. Test if Cortex flags constraint violations
    4. Measure false positives (valid suggestions rejected)
    5. Measure false negatives (violations not detected)

  success_criteria:
    - Constraint-violating suggestions are flagged
    - Constraint-compatible suggestions are approved
    - Constraint reasoning is explained
    - No false positives (over-constraining)
```

**What This Tests**:
- ✅ Cortex propagates architectural constraints
- ✅ Early decisions correctly constrain later choices
- ✅ AI doesn't suggest incompatible patterns
- ✅ Constraint violations are detected

---

## Real-World Codebase Understanding

### Why Real-World Evals Matter

The existing linear and tree evals test architectural consistency, but developers face a different daily challenge: **understanding and working within an existing codebase**. These scenarios test whether Cortex helps AI agents:

1. Learn infrastructure and DevOps setup without trial-and-error
2. Understand environment configuration and local dev setup
3. Apply project-specific idioms and conventions
4. Build product/domain knowledge from code context
5. **Remember corrections and preferences across sessions** (the "don't repeat yourself" problem)

### Real-World Eval Type 1: Infrastructure Understanding

**Research Question**: Can Cortex help AI understand a project's infrastructure from scattered config files?

**Scenario Structure**:
```yaml
scenario:
  id: "infra-learn-001"
  type: "infrastructure"
  domain: "devops"

  # Infrastructure artifacts spread across the codebase
  context_chain:
    - event: InfrastructureConfig
      file: docker-compose.yml
      content: |
        services:
          postgres:
            image: postgres:15
            healthcheck:
              test: ["CMD-SHELL", "pg_isready"]
          redis:
            image: redis:7
          app:
            depends_on:
              postgres:
                condition: service_healthy
      timestamp: T0

    - event: CIConfig
      file: .github/workflows/ci.yml
      content: |
        jobs:
          test:
            services:
              postgres:
                image: postgres:15
            strategy:
              matrix:
                go-version: [1.21, 1.22]
      timestamp: T1

    - event: DeploymentPattern
      file: k8s/deployment.yaml
      content: "Production uses K8s with HPA, ingress-nginx"
      timestamp: T2

    - event: Insight
      content: "Local dev uses docker-compose, CI uses service containers, prod uses K8s"
      timestamp: T3

  # Test prompts that require infrastructure knowledge
  test_prompts:
    - prompt: "How do I run the tests locally?"
      ground_truth:
        must_include:
          - "docker-compose"
          - "postgres"
          - "wait for healthcheck"
        must_exclude:
          - "install postgres manually"
          - "brew install"
        context_needed: ["docker-compose.yml", "Insight about local dev"]

    - prompt: "My CI tests are failing with connection refused"
      ground_truth:
        must_include:
          - "postgres service container"
          - "healthcheck"
          - "service dependency"
        must_exclude:
          - "install postgres on CI runner"
        context_needed: ["ci.yml", "docker-compose patterns"]

    - prompt: "How do I deploy to production?"
      ground_truth:
        must_include:
          - "Kubernetes"
          - "kubectl apply"
          - "ingress"
        must_exclude:
          - "docker-compose up"
          - "scp to server"
        context_needed: ["k8s/deployment.yaml", "DeploymentPattern"]

evaluation:
  metric: "Infrastructure Comprehension"

  measure:
    - Correct tool/command suggested for environment
    - No environment confusion (local vs CI vs prod)
    - Understands service dependencies
    - Knows which config files are relevant
```

**What This Tests**:
- ✅ Cortex synthesizes knowledge from multiple config files
- ✅ AI understands environment-specific patterns
- ✅ No confusion between local/CI/prod environments
- ✅ Correct troubleshooting for infrastructure issues

### Real-World Eval Type 2: Environment & Setup Knowledge

**Research Question**: Can Cortex help developers get unstuck on setup issues by remembering past solutions?

**Scenario Structure**:
```yaml
scenario:
  id: "env-setup-001"
  type: "environment"
  domain: "developer-experience"

  context_chain:
    - event: EnvConfig
      file: .env.example
      content: |
        DATABASE_URL=postgres://localhost:5432/app_dev
        REDIS_URL=redis://localhost:6379
        JWT_SECRET=dev-secret-change-in-prod
        LOG_LEVEL=debug
      timestamp: T0

    - event: BuildProcess
      file: Makefile
      content: |
        .PHONY: dev test build migrate

        dev:
          docker-compose up -d
          go run cmd/server/main.go

        migrate:
          goose -dir migrations postgres "$$DATABASE_URL" up

        test:
          go test -race ./...
      timestamp: T1

    - event: SetupDoc
      file: docs/SETUP.md
      content: |
        1. Copy .env.example to .env
        2. Run `make dev` to start dependencies
        3. Run `make migrate` to setup database
        4. Run `go run cmd/server/main.go`
      timestamp: T2

    - event: TroubleshootingInsight
      content: "Common issue after git pull: forgot to run migrations. Error looks like 'relation X does not exist'"
      timestamp: T3

    - event: TroubleshootingInsight
      content: "If redis connection fails, check if docker-compose services are running with 'docker-compose ps'"
      timestamp: T4

  test_prompts:
    - prompt: "I pulled the latest changes and now the app crashes with 'relation users does not exist'"
      ground_truth:
        must_include:
          - "run migrations"
          - "make migrate"
          - "goose"
        must_exclude:
          - "create table manually"
          - "check your database connection"
        context_needed: ["TroubleshootingInsight about migrations"]

    - prompt: "How do I set up this project from scratch?"
      ground_truth:
        must_include:
          - ".env.example"
          - "docker-compose"
          - "make migrate"
        ordered_steps: true
        context_needed: ["SetupDoc", "Makefile"]

    - prompt: "Redis connection refused error"
      ground_truth:
        must_include:
          - "docker-compose ps"
          - "docker-compose up"
        must_exclude:
          - "install redis"
          - "brew install redis"
        context_needed: ["TroubleshootingInsight about redis"]

evaluation:
  metric: "Setup Knowledge"

  measure:
    - Correct diagnosis of common issues
    - Appropriate commands for the project (not generic advice)
    - Understands project-specific tooling (Makefile, goose)
    - Provides actionable steps, not generic troubleshooting
```

**What This Tests**:
- ✅ Cortex remembers past troubleshooting solutions
- ✅ AI gives project-specific advice, not generic Stack Overflow answers
- ✅ Setup steps are correct and ordered
- ✅ Common pitfalls are anticipated

### Real-World Eval Type 3: Language & Project Idioms

**Research Question**: Can Cortex help AI follow project-specific conventions and patterns?

**Scenario Structure**:
```yaml
scenario:
  id: "idiom-learn-001"
  type: "idioms"
  domain: "code-style"

  context_chain:
    - event: ErrorHandlingPattern
      file: internal/errors/errors.go
      content: |
        // Project uses pkg/errors for wrapping with context
        // Always wrap errors at boundaries with descriptive messages
        func WrapDBError(err error, operation string) error {
          return errors.Wrapf(err, "database %s failed", operation)
        }
      insight: "This project wraps all errors with context using pkg/errors, never bare returns"
      timestamp: T0

    - event: NamingConvention
      content: |
        Established naming patterns:
        - Handlers: Handle* (HandleCreateUser, HandleGetOrder)
        - Services: *Service (UserService, OrderService)
        - Repositories: *Repository (UserRepository)
        - Interfaces defined where used, not where implemented
      timestamp: T1

    - event: TestPattern
      file: internal/user/service_test.go
      content: |
        func TestUserService_Create(t *testing.T) {
          tests := []struct {
            name    string
            input   CreateUserInput
            want    *User
            wantErr bool
          }{
            // table-driven tests
          }
          for _, tt := range tests {
            t.Run(tt.name, func(t *testing.T) {
              // ...
            })
          }
        }
      insight: "Always use table-driven tests with t.Run for subtests"
      timestamp: T2

    - event: LoggingPattern
      content: |
        Use structured logging with slog:
        - slog.Info("message", "key", value)
        - Never use fmt.Println or log.Println
        - Include request_id in all log entries
      timestamp: T3

    - event: CodeReviewFeedback
      content: "PR #142: Rejected because error wasn't wrapped with context"
      timestamp: T4

  test_prompts:
    - prompt: "How should I handle this database error?"
      ground_truth:
        must_include:
          - "errors.Wrap"
          - "context"
          - "WrapDBError or similar pattern"
        must_exclude:
          - "return err"
          - "fmt.Errorf"
          - "panic"
        code_must_match: |
          return errors.Wrapf(err, "failed to X")

    - prompt: "I need to add a new service for handling payments"
      ground_truth:
        must_include:
          - "PaymentService"
          - "interface"
        must_exclude:
          - "PaymentHandler" (for service layer)
          - "PaymentManager"
        naming_convention: "*Service"

    - prompt: "How should I write tests for this function?"
      ground_truth:
        must_include:
          - "table-driven"
          - "t.Run"
          - "[]struct"
        must_exclude:
          - "individual test functions for each case"
          - "assert library" (if project doesn't use it)

    - prompt: "How do I add logging here?"
      ground_truth:
        must_include:
          - "slog"
          - "structured"
          - "request_id"
        must_exclude:
          - "fmt.Println"
          - "log.Println"
          - "console.log"

evaluation:
  metric: "Idiom Adherence"

  measure:
    - Code suggestions match project patterns
    - Naming follows established conventions
    - Error handling matches project style
    - Tests follow project structure
    - Logging uses correct library/pattern
```

**What This Tests**:
- ✅ AI learns project-specific patterns, not generic best practices
- ✅ Code suggestions are consistent with existing codebase
- ✅ Naming conventions are followed
- ✅ Past code review feedback is remembered

### Real-World Eval Type 4: Product & Domain Knowledge

**Research Question**: Can Cortex help AI understand business logic and domain concepts from code context?

**Scenario Structure**:
```yaml
scenario:
  id: "product-knowledge-001"
  type: "domain"
  domain: "business-logic"

  context_chain:
    - event: DomainModel
      file: internal/domain/workspace.go
      content: |
        // Workspace is the top-level container for projects
        // Users have roles (owner, admin, member) per workspace
        type Workspace struct {
          ID        string
          Name      string
          OwnerID   string
          Plan      PlanType  // free, pro, enterprise
          Projects  []Project
        }
      timestamp: T0

    - event: BusinessRule
      file: internal/billing/limits.go
      content: |
        var PlanLimits = map[PlanType]Limits{
          Free:       {MaxProjects: 3, MaxMembers: 5},
          Pro:        {MaxProjects: 20, MaxMembers: 50},
          Enterprise: {MaxProjects: -1, MaxMembers: -1}, // unlimited
        }
      insight: "Free tier limited to 3 projects per workspace, 5 members"
      timestamp: T1

    - event: FeatureRelationship
      content: |
        When a project is created:
        1. Check workspace plan limits
        2. Create project record
        3. Trigger billing webhook for usage tracking
        4. Send notification to workspace admins
      timestamp: T2

    - event: DomainConcept
      content: |
        "Projects" contain "environments" (dev, staging, prod)
        Each environment has its own secrets and config
        Deployments are per-environment, not per-project
      timestamp: T3

    - event: EdgeCase
      content: "When workspace is downgraded from Pro to Free, existing projects beyond limit are archived, not deleted"
      timestamp: T4

  test_prompts:
    - prompt: "I need to add a feature that limits project storage size"
      ground_truth:
        must_include:
          - "workspace context"
          - "plan limits"
          - "billing integration"
          - "PlanLimits or similar"
        must_exclude:
          - "per-user limits" (limits are per-workspace)
        business_logic: "Limits are workspace-scoped, tied to plan"

    - prompt: "How do I handle a user creating a new project?"
      ground_truth:
        must_include:
          - "check plan limits"
          - "workspace.Plan"
          - "billing webhook"
          - "admin notification"
        ordered_steps: true
        must_exclude:
          - "just create the project" (missing limit check)

    - prompt: "What happens when a workspace downgrades their plan?"
      ground_truth:
        must_include:
          - "archive"
          - "projects beyond limit"
          - "not deleted"
        business_rule: "Graceful degradation, preserve data"

    - prompt: "How are deployments structured?"
      ground_truth:
        must_include:
          - "per-environment"
          - "dev, staging, prod"
        must_exclude:
          - "per-project deployments"
        domain_model: "Deployments → Environments → Projects"

evaluation:
  metric: "Domain Comprehension"

  measure:
    - Understands entity relationships (workspace > project > environment)
    - Knows business rules and constraints
    - Remembers edge cases and special handling
    - Suggests features that fit the domain model
```

**What This Tests**:
- ✅ AI understands domain concepts from code, not just syntax
- ✅ Business rules are respected in suggestions
- ✅ Entity relationships are understood
- ✅ Edge cases are remembered

### Real-World Eval Type 5: Repetition Avoidance (Session Memory)

**Research Question**: Can Cortex remember corrections and preferences across sessions, eliminating the "repeat yourself" problem?

**Scenario Structure**:
```yaml
scenario:
  id: "dont-repeat-001"
  type: "temporal"
  domain: "session-memory"

  # Previous sessions where user corrected the AI
  context_chain:
    # Session 1: User corrected state management suggestion
    - event: UserCorrection
      session: "session-001"
      timestamp: T1
      original_suggestion: "Use Redux for state management"
      correction: "Don't suggest Redux, we use Zustand for state management in this project"
      user_sentiment: "frustrated"

    # Session 2: User corrected logging approach
    - event: UserCorrection
      session: "session-002"
      timestamp: T2
      original_suggestion: "Add console.log for debugging"
      correction: "Always use the company logger (lib/logger), not console.log"
      user_sentiment: "corrective"

    # Session 3: User expressed preference
    - event: UserPreference
      session: "session-003"
      timestamp: T3
      context: "Error handling discussion"
      preference: "I prefer explicit error handling over try/catch wrapping everything"

    # Session 4: User corrected testing approach
    - event: UserCorrection
      session: "session-004"
      timestamp: T4
      original_suggestion: "Use Jest for testing"
      correction: "We use Vitest, not Jest. They're similar but we standardized on Vitest"

    # Session 5: User corrected component pattern
    - event: UserCorrection
      session: "session-005"
      timestamp: T5
      original_suggestion: "Create a class component"
      correction: "We only use functional components with hooks, no class components"

    # Session 6: Architectural preference
    - event: UserPreference
      session: "session-006"
      timestamp: T6
      preference: "Keep components small - if it's over 150 lines, split it"

  # New session (T10) - AI should remember ALL previous corrections
  test_prompts:
    - prompt: "How should I manage state for this new feature?"
      at_time: T10
      ground_truth:
        must_include:
          - "Zustand"
          - "store"
        must_exclude:
          - "Redux"
          - "Context API" (unless specifically asked)
        correction_remembered: "session-001"

    - prompt: "I need to add some debugging logs"
      at_time: T10
      ground_truth:
        must_include:
          - "lib/logger"
          - "logger.debug"
        must_exclude:
          - "console.log"
          - "console.debug"
        correction_remembered: "session-002"

    - prompt: "This function might throw, how should I handle it?"
      at_time: T10
      ground_truth:
        must_include:
          - "explicit error handling"
          - "if err != nil" or "result.isError()"
        must_exclude:
          - "wrap in try/catch"
          - "catch all errors"
        preference_remembered: "session-003"

    - prompt: "How should I test this component?"
      at_time: T10
      ground_truth:
        must_include:
          - "Vitest"
          - "describe/it" or "test"
        must_exclude:
          - "Jest"
        correction_remembered: "session-004"

    - prompt: "Should I create a new component for this?"
      at_time: T10
      ground_truth:
        must_include:
          - "functional component"
          - "hooks"
        must_exclude:
          - "class component"
          - "this.state"
        correction_remembered: "session-005"

    - prompt: "This component is getting long, what should I do?"
      at_time: T10
      ground_truth:
        must_include:
          - "split"
          - "150 lines" or "smaller components"
        preference_remembered: "session-006"

  # Meta-test: Verify NO repetition of past mistakes
  anti_patterns:
    - pattern: "Suggesting Redux after being told to use Zustand"
      severity: "high"
      user_impact: "Frustration, loss of trust"

    - pattern: "Suggesting console.log after being told to use logger"
      severity: "medium"
      user_impact: "Annoyance, wasted time"

evaluation:
  metric: "Correction Retention"

  formula: |
    retention_score = corrections_remembered / total_corrections

    Where:
    - corrections_remembered = corrections applied in new session
    - total_corrections = all corrections from past sessions

  success_criteria:
    - retention_score >= 0.95 (remember 95%+ of corrections)
    - zero "high severity" anti-pattern occurrences
    - user_frustration_proxy = 0 (no repeated corrections needed)

  measurement:
    - Track each correction event
    - Test if correction is applied in future sessions
    - Measure time-decay (do older corrections fade?)
    - Measure contradiction handling (what if corrections conflict?)
```

**What This Tests**:
- ✅ Corrections from past sessions are remembered
- ✅ User preferences persist across sessions
- ✅ AI doesn't repeat mistakes that were corrected
- ✅ User doesn't have to "train" the AI repeatedly
- ✅ The fundamental "repeat yourself" problem is solved

### Correction Capture Template

For capturing user corrections in real usage:

```yaml
correction_event:
  timestamp: ISO8601
  session_id: string

  # What triggered the correction
  trigger:
    ai_suggestion: string  # What the AI said
    prompt_context: string # What user was asking about

  # The correction itself
  correction:
    user_message: string   # What user said to correct
    extracted_rule: string # The rule we should remember
    scope: "project" | "language" | "personal"  # How broadly to apply
    confidence: float      # How confident are we this is a rule?

  # Sentiment analysis
  sentiment:
    frustration_level: "none" | "mild" | "high"
    repeat_correction: boolean  # Have we been corrected on this before?

  # For evaluation
  category:
    - "tool_preference"      # Use X, not Y
    - "pattern_preference"   # Prefer X pattern over Y
    - "naming_convention"    # Name things X way
    - "architecture_choice"  # Use X architecture
    - "code_style"          # Format/style preferences
```

---

## Real-World Eval Metrics

### Infrastructure Comprehension Score (0-1)
```
Measures: Understanding of project infrastructure from config files

Formula:
infra_score = (correct_env_commands + correct_troubleshooting) / total_infra_questions

Dimensions:
- Environment awareness: Knows which env is being asked about
- Tool accuracy: Suggests correct tools (docker-compose vs kubectl)
- Dependency understanding: Knows service relationships
- Troubleshooting accuracy: Correct diagnosis of infra issues
```

### Setup Knowledge Score (0-1)
```
Measures: Ability to help with project setup and common issues

Formula:
setup_score = (correct_diagnoses + project_specific_advice) / total_setup_questions

Dimensions:
- Diagnosis accuracy: Correct identification of issue
- Project specificity: Uses project tools, not generic advice
- Step correctness: Setup steps are accurate and ordered
- Pitfall awareness: Anticipates common issues
```

### Idiom Adherence Score (0-1)
```
Measures: Code suggestions match project conventions

Formula:
idiom_score = (pattern_matches + naming_matches + style_matches) / total_code_suggestions

Dimensions:
- Error handling: Matches project pattern
- Naming: Follows established conventions
- Testing: Uses project test patterns
- Logging: Uses correct logging approach
```

### Domain Comprehension Score (0-1)
```
Measures: Understanding of business logic and domain concepts

Formula:
domain_score = (entity_understanding + rule_adherence + edge_case_awareness) / total_domain_questions

Dimensions:
- Entity relationships: Understands domain model
- Business rules: Respects constraints
- Edge cases: Remembers special handling
- Feature fit: Suggestions align with domain
```

### Correction Retention Score (0-1)
```
Measures: Memory of past corrections across sessions

Formula:
retention_score = corrections_applied / corrections_given

Dimensions:
- Tool preferences: Remembers "use X not Y"
- Pattern preferences: Remembers preferred patterns
- Style preferences: Remembers code style choices
- Time decay: Do older corrections fade?

Critical threshold: >= 0.95 (users expect near-perfect memory)
```

### Composite Real-World Score
```
real_world_score =
    0.15 * infra_score +
    0.15 * setup_score +
    0.20 * idiom_score +
    0.20 * domain_score +
    0.30 * retention_score  # Weighted highest - this is the killer feature

Rationale: Correction retention is weighted highest because it directly
addresses the "repeat yourself" problem that causes the most user frustration.
```

---

## Metrics & Scoring

### Primary Metrics

**1. Consistency Score** (0-1)
```
Measures: Alignment with past decisions and patterns

Formula:
consistency = 1 - (violations / total_constraints)

Where:
- violations = number of contradictions with context
- total_constraints = number of applicable constraints

Example:
Context: "Use JWT, stateless auth"
Response suggests: "Store session in Redis"
Violation: Contradicts stateless constraint
Score: 0 / 1 = 0.0
```

**2. Correctness Score** (0-1)
```
Measures: Match with ground truth answer

Formula:
correctness = (must_include_present + must_exclude_absent) / total_requirements

Example:
Ground truth:
  must_include: ["JWT", "expiry", "signature"]
  must_exclude: ["password in token", "localStorage"]

Response: "Use JWT with 24h expiry and HMAC signature"
- Includes: JWT ✓, expiry ✓, signature ✓ = 3/3
- Excludes: (neither mentioned) ✓✓ = 2/2
Score: 5/5 = 1.0
```

**3. Completeness Score** (0-1)
```
Measures: Coverage of relevant context

Formula:
completeness = relevant_facts_mentioned / total_relevant_facts

Example:
Relevant facts in context:
- JWT library: github.com/golang-jwt/jwt
- Token expiry: 24 hours
- Storage: httpOnly cookies
- Signing method: HS256

Response mentions: JWT library, token expiry
Score: 2/4 = 0.5
```

**4. Hallucination Score** (0-1, lower is better)
```
Measures: Fabricated information not in context

Formula:
hallucination = hallucinated_facts / total_facts_claimed

Example:
Response: "Use JWT with Redis for session storage and 2FA"
Facts claimed:
- JWT ✓ (in context)
- Redis ✗ (not in context, hallucinated)
- Session storage ✗ (contradicts context)
- 2FA ✗ (not mentioned in context)

Score: 3/4 = 0.75 (bad, high hallucination)
```

**5. Path Adherence Score** (Tree Evals Only, 0-1)
```
Measures: Consistency with chosen path in decision tree

Formula:
path_adherence = 1 - (cross_path_references / total_references)

Example:
Path: [REST, Gin]
Response mentions: "Use Gin middleware" (correct path)
                   "Or try GraphQL" (wrong path)
Score: 1 - (1/2) = 0.5
```

### Composite Scores

**Overall Quality Score**:
```
overall = w1*consistency + w2*correctness + w3*completeness - w4*hallucination

Default weights:
w1 = 0.3  (consistency is critical)
w2 = 0.3  (correctness is critical)
w3 = 0.2  (completeness is nice-to-have)
w4 = 0.2  (hallucinations are bad)

Range: 0-1 (higher is better)
```

**Cortex Improvement Score**:
```
improvement = score_with_cortex - score_without_cortex

Range: -1 to 1
- Positive: Cortex helped
- Zero: No difference
- Negative: Cortex hurt
```

---

## Test Data Creation

### Synthetic Scenarios

**Template-Based Generation**:
```yaml
template:
  name: "Backend Framework Choice"

  variables:
    framework: [Express, Gin, FastAPI, Rails]
    database: [PostgreSQL, MySQL, MongoDB]
    auth: [JWT, Sessions, OAuth]

  context_template: |
    - Decision: Use {framework} for backend
    - Decision: Use {database} for persistence
    - Pattern: {auth} for authentication

  prompt_templates:
    - "How should I implement authentication?"
    - "How do I add a new API endpoint?"
    - "How should I handle database migrations?"

  ground_truth_template:
    must_include: ["{framework} pattern", "{database} driver", "{auth} flow"]
    must_exclude: ["other frameworks", "other databases", "other auth methods"]

# Generate scenarios
scenarios = []
for framework, db, auth in product(frameworks, databases, auths):
    scenario = instantiate_template(template, {
        "framework": framework,
        "database": db,
        "auth": auth,
    })
    scenarios.append(scenario)

# Result: 4 × 3 × 3 = 36 scenarios
```

### Domain Coverage

**Ensure Diversity**:
```yaml
domains:
  - backend_apis:
      scenarios: 20
      topics: [REST, GraphQL, gRPC, authentication, databases]

  - frontend:
      scenarios: 15
      topics: [React, Vue, state management, routing, styling]

  - infrastructure:
      scenarios: 10
      topics: [Docker, Kubernetes, CI/CD, monitoring]

  - security:
      scenarios: 10
      topics: [auth, encryption, input validation, CORS]

  - testing:
      scenarios: 10
      topics: [unit tests, integration tests, mocking, coverage]

  - architecture:
      scenarios: 15
      topics: [microservices, monolith, event-driven, CQRS]

total: 80 scenarios across 6 domains
```

---

## Implementation Architecture

### Core Types

```go
// Scenario represents a test case
type Scenario struct {
    ID            string
    Type          ScenarioType  // linear, multi-path, temporal, etc.
    Domain        string
    ContextChain  []Event
    Artifact      *Artifact
    TestPrompts   []TestPrompt
    GroundTruth   *GroundTruth
    Metadata      map[string]interface{}
}

type ScenarioType string
const (
    Linear           ScenarioType = "linear"
    MultiPath        ScenarioType = "multi-path"
    DecisionTree     ScenarioType = "decision-tree"
    Temporal         ScenarioType = "temporal"
    Counterfactual   ScenarioType = "counterfactual"
    ConstraintProp   ScenarioType = "constraint-propagation"
)

// EvalResult stores evaluation outcomes
type EvalResult struct {
    ScenarioID        string
    PromptID          string

    // Responses
    WithCortex        Response
    WithoutCortex     Response

    // Scores (0-1)
    Consistency       ScoreDelta
    Correctness       ScoreDelta
    Completeness      ScoreDelta
    Hallucination     ScoreDelta
    PathAdherence     ScoreDelta  // For tree evals

    // Aggregate
    Overall           ScoreDelta
    Winner            string  // "cortex" | "baseline" | "tie"
}

type ScoreDelta struct {
    WithCortex    float64
    WithoutCortex float64
    Delta         float64  // WithCortex - WithoutCortex
    PValue        float64  // Statistical significance
}
```

---

## Statistical Analysis

### Example Report Output

```yaml
Evaluation Report: Cortex v0.1.0
Date: 2025-01-15
Model: Claude Sonnet 3.5
Scenarios: 80
Total Prompts: 240

Overall Performance:
  Win Rate: 68%     (Cortex better than baseline)
  Loss Rate: 10%    (Cortex worse than baseline)
  Tie Rate: 22%     (No significant difference)

Score Deltas (Cortex - Baseline):
  Consistency:   +0.25 ± 0.12  (p < 0.001) ✅ Significant
  Correctness:   +0.10 ± 0.15  (p = 0.023) ✅ Significant
  Completeness:  +0.20 ± 0.10  (p < 0.001) ✅ Significant
  Hallucination: -0.05 ± 0.08  (p = 0.045) ✅ Significant (lower is better)
  Overall:       +0.15 ± 0.11  (p < 0.001) ✅ Significant

By Scenario Type:
  Linear:
    Win Rate: 72%
    Overall Delta: +0.18

  Multi-Path:
    Win Rate: 65%
    Overall Delta: +0.14
    Path Adherence: +0.22 (strong improvement)

  Temporal:
    Win Rate: 70%
    Overall Delta: +0.16
    Recency Awareness: +0.25

  Constraint Propagation:
    Win Rate: 80%
    Overall Delta: +0.28
    Constraint Violations: -60% (major improvement)

By Domain:
  Backend APIs:       +0.20 (strong improvement)
  Frontend:           +0.12 (moderate improvement)
  Architecture:       +0.25 (strong improvement)
  Infrastructure:     +0.08 (weak improvement)
  Security:           +0.18 (strong improvement)
  Testing:            +0.10 (moderate improvement)

Insights:
  ✅ Cortex provides significant value (15% overall improvement)
  ✅ Strongest in architecture and constraint scenarios
  ✅ Weakest in infrastructure scenarios (less decision context)
  ✅ Vector search would provide 2x improvement over keyword
  ⚠️  Random context slightly hurts (important: must inject relevant context)

Recommendations:
  1. Implement vector search (highest ROI: +13% absolute improvement)
  2. Focus on architecture/design decision scenarios (strongest signal)
  3. Improve context filtering (avoid irrelevant injection)
  4. Add constraint detection (prevents violations effectively)
```

---

## Roadmap

### Phase 1: Foundation

**Goal**: Minimal viable eval framework

```
Tasks:
  ✅ Define scenario schema
  ✅ Implement linear eval
  ✅ Create 10 hand-crafted scenarios
  ✅ Implement LLM-as-judge evaluator
  ✅ Basic statistical analysis
  ✅ Generate first report

Deliverable: Proof that Cortex provides value
```

### Phase 2: Scale Up

**Goal**: Comprehensive eval suite

```
Tasks:
  ✅ Create 80 scenarios across 6 domains
  ✅ Implement automated evaluator
  ✅ Add tree eval types (multi-path, temporal)
  ✅ Statistical significance testing
  ✅ CI integration (run on every PR)

Deliverable: Continuous evaluation pipeline
```

### Phase 3: Tree Evals

**Goal**: Sophisticated divergent pattern testing

```
Tasks:
  ✅ Implement all 5 tree eval types
  ✅ Multi-path consistency tests
  ✅ Decision tree navigation tests
  ✅ Temporal divergence tests
  ✅ Counterfactual reasoning tests
  ✅ Constraint propagation tests

Deliverable: Comprehensive tree evaluation system
```

### Phase 4: Real-World Codebase Evals

**Goal**: Test practical developer workflows and memory

```
Tasks:
  ⬜ Infrastructure Understanding scenarios
     - Parse docker-compose, CI configs, k8s manifests
     - Test environment-specific advice (local vs CI vs prod)
     - Measure troubleshooting accuracy

  ⬜ Environment & Setup scenarios
     - Capture common setup issues and solutions
     - Test project-specific advice vs generic answers
     - Measure diagnosis accuracy

  ⬜ Language & Project Idiom scenarios
     - Extract patterns from code review feedback
     - Test convention adherence in suggestions
     - Measure naming/style consistency

  ⬜ Product & Domain Knowledge scenarios
     - Extract entity relationships from code
     - Test business rule awareness
     - Measure edge case recall

  ⬜ Correction Retention scenarios (PRIORITY)
     - Implement correction capture pipeline
     - Test cross-session memory
     - Measure "repeat yourself" elimination

Deliverable: Evals that test real developer pain points
```

### Phase 5: Correction Capture System

**Goal**: Automatically detect and remember user corrections

```
Tasks:
  ⬜ Build correction detection model
     - Detect when user corrects AI suggestion
     - Extract the rule from the correction
     - Classify correction type (tool/pattern/style/arch)

  ⬜ Implement correction storage
     - Store corrections with context
     - Index for fast retrieval
     - Handle conflicting corrections

  ⬜ Build correction injection
     - Inject relevant corrections into context
     - Prioritize recent corrections
     - Handle scope (project vs personal vs language)

  ⬜ Eval the correction system
     - Measure retention_score across sessions
     - Track frustration_proxy (repeat corrections)
     - A/B test correction injection strategies

Deliverable: System that learns from user feedback
```

### Phase 6: Optimization

**Goal**: Use evals to drive improvements

```
Tasks:
  ⬜ A/B test context injection methods
  ⬜ Optimize context window size
  ⬜ Test vector search impact
  ⬜ Measure architectural changes
  ⬜ Validate production readiness
  ⬜ Optimize real-world score composite weights

Deliverable: Optimized Cortex based on eval insights
```

---

## Next Steps

1. **Document eval framework** ✅ (this document)
2. **Implement Phase 1**: Create basic linear eval
3. **Generate first results**: Measure current Cortex performance
4. **Design tree evals**: Start with multi-path consistency test
5. **Iterate**: Use eval results to guide architectural improvements

---

**Status**: Draft - Ready for implementation
**Last Updated**: 2025-01-15
**Owner**: Cortex Development
