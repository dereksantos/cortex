// Package llm — role-map recommender.
//
// Given a set of detected endpoints + their model catalogs, propose a
// per-role assignment biased toward strongest LOCAL model unless no
// local option has the required capability. This is the engine behind
// the onboarding flow described in ROADMAP.md "Onboarding as the thesis
// surface" — the visible expression of the multi-model leverage claim.
//
// Phase 4 Slice D.

package llm

import (
	"sort"
	"strconv"
	"strings"
)

// Role enumerates the per-role assignments the harness routes to. The
// canonical set matches docs/eval-strategy.md's role taxonomy.
type Role string

const (
	RoleCode   Role = "code"
	RoleReason Role = "reason"
	RoleFast   Role = "fast"
	RoleEmbed  Role = "embed"
	RoleRerank Role = "rerank"
)

// AllRoles is the canonical list. Order matters for display only.
var AllRoles = []Role{RoleCode, RoleReason, RoleFast, RoleEmbed, RoleRerank}

// EndpointCatalog is one endpoint's name + its discovered model list.
// Matches the shape returned by the detection layer; restated here so
// the recommender stays decoupled from the detection package (which
// lives under internal/).
type EndpointCatalog struct {
	Name    string
	BaseURL string
	IsLocal bool // true for local endpoints (chatterbox/Lemonade, LM Studio, vLLM, Ollama)
	Models  []CompatModel
}

// RoleChoice is the recommender's pick for one role.
type RoleChoice struct {
	Endpoint string
	Model    string
	Reason   string // one-line human-readable rationale
}

// Recommendation is the full role-map proposal across all roles. A role
// can be absent from Choices when no detected model has the required
// capability — caller prints "no candidate" or prompts the user.
type Recommendation struct {
	Choices map[Role]RoleChoice
}

// Recommend builds a role-map proposal from detected endpoint catalogs.
// Strategy:
//
//   - For each role, score every (endpoint, model) candidate that has
//     the required capability tag.
//   - Score prefers local-with-required-capability; cloud is fallback.
//   - Within a tier, prefers larger context windows (rough proxy for
//     model "strength" — explicit param count isn't always reliable from
//     endpoint metadata).
//   - Smaller models win for the "fast" role specifically.
//
// Recommendations are advisory; the caller (onboarding flow) shows the
// reasoning and lets the user override before persisting.
func Recommend(catalogs []EndpointCatalog) Recommendation {
	rec := Recommendation{Choices: map[Role]RoleChoice{}}
	for _, role := range AllRoles {
		if pick, ok := recommendOne(role, catalogs); ok {
			rec.Choices[role] = pick
		}
	}
	return rec
}

// recommendOne picks the best (endpoint, model) for one role. Returns
// false when no candidate has the required capability.
func recommendOne(role Role, catalogs []EndpointCatalog) (RoleChoice, bool) {
	requiredCap, ok := roleCapability(role)
	if !ok {
		return RoleChoice{}, false
	}

	type candidate struct {
		endpoint string
		model    CompatModel
		isLocal  bool
	}
	var cands []candidate
	for _, ep := range catalogs {
		for _, m := range ep.Models {
			if !HasCapability(m, requiredCap) {
				continue
			}
			cands = append(cands, candidate{
				endpoint: ep.Name,
				model:    m,
				isLocal:  ep.IsLocal,
			})
		}
	}
	if len(cands) == 0 {
		return RoleChoice{}, false
	}

	preferLarger := role != RoleFast
	sort.SliceStable(cands, func(i, j int) bool {
		// Local wins over cloud.
		if cands[i].isLocal != cands[j].isLocal {
			return cands[i].isLocal
		}
		// Within tier, prefer larger (or smaller for fast).
		si := modelSize(cands[i].model)
		sj := modelSize(cands[j].model)
		if si != sj {
			if preferLarger {
				return si > sj
			}
			return si < sj
		}
		// Stable tiebreak by id for deterministic output.
		return cands[i].model.ID < cands[j].model.ID
	})

	best := cands[0]
	return RoleChoice{
		Endpoint: best.endpoint,
		Model:    best.model.ID,
		Reason:   recommendReason(role, best.isLocal, best.model),
	}, true
}

// roleCapability returns the capability label a role requires.
func roleCapability(r Role) (string, bool) {
	switch r {
	case RoleCode:
		return CapCoding, true
	case RoleReason:
		return CapReasoning, true
	case RoleFast:
		return CapToolCalling, true
	case RoleEmbed:
		return CapEmbedding, true
	case RoleRerank:
		return CapReranking, true
	}
	return "", false
}

// modelSize returns a sortable "size" estimate for a model. Combines
// param-count from the id ("7b", "30b", etc.) when present with the
// context window as a secondary signal. Returns 0 when both are
// unknown — those models sort to the end via stable sort.
func modelSize(m CompatModel) int {
	if p := parseParamCount(m.ID); p > 0 {
		// Param count in millions: 7b = 7000, 30b = 30000. This puts
		// it on a comparable scale to context windows (which are
		// already in token-thousands ≈ 1000s of units).
		return p * 1000
	}
	return m.ContextLength
}

// parseParamCount extracts a parameter count (in billions, returned as
// the integer billions) from a model id. Recognizes "-30b-", "30B",
// "0.6b", "1.5b", etc. Returns 0 when no count is detectable.
func parseParamCount(modelID string) int {
	lower := strings.ToLower(modelID)
	// Walk through looking for "<digit><b>" patterns. Conservative —
	// only matches digits-then-b separated by non-alphanumeric chars.
	for i := 0; i < len(lower); i++ {
		if !isDigit(lower[i]) {
			continue
		}
		// Read up to next non-digit-non-dot.
		j := i
		for j < len(lower) && (isDigit(lower[j]) || lower[j] == '.') {
			j++
		}
		if j >= len(lower) || lower[j] != 'b' {
			i = j
			continue
		}
		// Boundary check: char after 'b' must be non-alphanumeric or end-of-string.
		if j+1 < len(lower) && isAlphanum(lower[j+1]) {
			i = j
			continue
		}
		// Boundary check on left: char before digit (if any) must be non-alphanumeric.
		if i > 0 && isAlphanum(lower[i-1]) {
			i = j
			continue
		}
		num, err := strconv.ParseFloat(lower[i:j], 64)
		if err == nil && num > 0 {
			return int(num)
		}
		i = j
	}
	return 0
}

func isDigit(b byte) bool    { return b >= '0' && b <= '9' }
func isAlphanum(b byte) bool { return isDigit(b) || (b >= 'a' && b <= 'z') }

// recommendReason builds a one-line human-readable rationale for a
// pick. Shown in the onboarding-flow output so the user can sanity-
// check the recommender.
func recommendReason(role Role, isLocal bool, m CompatModel) string {
	loc := "cloud"
	if isLocal {
		loc = "local"
	}
	caps := strings.Join(EffectiveLabels(m), ",")
	switch role {
	case RoleFast:
		return loc + " · smallest tool-caller · " + caps
	case RoleEmbed, RoleRerank:
		return loc + " · " + caps
	default:
		return loc + " · " + caps
	}
}
