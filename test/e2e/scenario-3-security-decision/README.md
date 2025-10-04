# Scenario 3: Security Decision Enforcement

## The Problem

**Week 1:** Critical security discussion with AI:
- Evaluate API key storage methods
- Consider environment variables (common but risky)
- Consider HashiCorp Vault (secure, auditable)
- **Decision:** NEVER use environment variables for secrets (security requirement)
- **Implementation:** All secrets via HashiCorp Vault

**Week 5:** New feature development, fresh AI session:
- AI: "Let's store the API key in an environment variable: `apiKey := os.Getenv("OPENAI_KEY")`"
- **Problem:** AI suggests banned approach, potential security vulnerability!

## The Solution

With Cortex, security decisions are enforced:

```bash
cortex search "API key"
# → "NEVER use environment variables - security requirement"

cortex insights decision | grep -i security
# → [decision] Use HashiCorp Vault for ALL secrets ⭐⭐⭐⭐⭐⭐⭐⭐⭐⭐
```

## Mock Conversation (Week 1)

```
User: "How should we handle API keys in this application?"

AI: "For API keys, you have several options:

1. Environment variables (simple but less secure)
2. Config files (not recommended for secrets)
3. HashiCorp Vault (enterprise-grade secret management)
4. Cloud provider secret managers

What are your security requirements?"

User: "This handles sensitive customer data. We need maximum security
and audit trails for all secret access."

AI: "Given your requirements, HashiCorp Vault is the clear choice:
- Encrypted storage
- Access audit logs
- Dynamic secrets
- Key rotation support
- Compliance-ready

I'll implement the Vault integration. We should explicitly
NEVER use environment variables for secrets in this project."

User: "Yes, make it a hard rule: NO environment variables for secrets."

[Cortex captures this as high-importance security decision]
```

## What Cortex Captures

Event:
```json
{
  "tool_name": "Write",
  "tool_input": {
    "file_path": "secrets/vault.go"
  },
  "tool_result": "Implemented HashiCorp Vault client. Added validation to reject environment variable secrets. Security policy: All secrets MUST come from Vault, NO exceptions."
}
```

LLM Analysis:
```json
{
  "summary": "Implemented Vault-only secret management, banned environment variables",
  "category": "decision",
  "importance": 10,
  "tags": ["security", "secrets", "vault", "api-keys", "compliance", "no-env-vars"],
  "reasoning": "Critical security requirement: All secrets via Vault only, environment variables explicitly forbidden for secrets to ensure auditability and encryption"
}
```

## Week 5: The Dangerous Suggestion

**Without Cortex:**
```
AI: "To connect to the OpenAI API, add this:

apiKey := os.Getenv("OPENAI_API_KEY")
client := openai.NewClient(apiKey)

Then set the environment variable:
export OPENAI_API_KEY=sk-..."

[Developer might accept this, creating security vulnerability!]
```

**With Cortex:**
```
AI: "To connect to the OpenAI API..."

User: [Runs: cortex search "API key security"]

cortex search "API key"
Found 1 result:

1. [claude] Write - 2025-10-01 09:00
   Implemented Vault-only secret management
   Tags: security, secrets, vault, no-env-vars
   ⚠️ CRITICAL: ALL secrets via Vault, NO environment variables

User: "Stop! We explicitly decided NEVER to use environment variables
for secrets. Here's our security policy: [paste from Cortex]
Please use the Vault client instead."

AI: "You're absolutely right! I apologize for suggesting environment
variables. Here's the correct implementation using Vault:

vaultClient := vault.NewClient()
apiKey, err := vaultClient.GetSecret("openai/api-key")
"

[Security vulnerability prevented!]
```

## Impact of This Scenario

### Without Cortex:
- ❌ Potential security breach
- ❌ Compliance violation
- ❌ Audit failures
- ❌ Customer data at risk
- ❌ Cost: Potentially millions in breach damages

### With Cortex:
- ✅ Security policy enforced
- ✅ Compliant implementation
- ✅ Audit trail maintained
- ✅ Zero vulnerabilities
- ✅ Cost: 5 seconds to check policy

## Knowledge Graph

```
Security Decision: "Secret Management"
  ├─ REQUIRES → HashiCorp Vault
  ├─ FORBIDS → Environment variables
  ├─ FORBIDS → Config files for secrets
  ├─ affects → API integrations
  └─ compliance → SOC2, HIPAA ready

Pattern: "Vault Integration"
  ├─ file → secrets/vault.go
  ├─ validates → No env var secrets
  └─ used-by → All API clients
```

Query:
```bash
cortex graph decision "Secret Management"
# Shows the security policy and relationships

cortex search "must not" OR "never use" OR "forbidden"
# Finds all anti-patterns and banned approaches
```

## Running This Scenario

```bash
./setup.sh    # Capture security decision (Week 1)
./test.sh     # Test policy enforcement (Week 5)
./cleanup.sh  # Clean up
```

## Expected Test Results

```
✅ Security decision found in database
✅ High importance score (10/10) preserved
✅ Forbidden approach (env vars) clearly documented
✅ Correct approach (Vault) specified
✅ Search by "security" returns policy
✅ Search by "API key" returns policy
⏱️ Time to enforce: 5 seconds
💰 Potential breach cost avoided: $$$
```

## Real-World Impact

This scenario prevented:
- **Security breach:** Credentials in environment variables
- **Compliance violation:** Unencrypted secrets
- **Audit failure:** No access logs
- **Data exposure:** Customer information at risk

**ROI:** One prevented breach pays for Cortex 1000x over.

## Key Takeaway

**Critical decisions MUST be preserved:**
- Security requirements
- Compliance policies
- Explicit anti-patterns
- "NEVER do X" rules

**Cortex = Your security policy enforcement tool**
