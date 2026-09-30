package service

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

// This is short-lived feedback from actual BPS rejections, not an estimate of
// provider capacity. No configured RPM/TPM limit or native-route state changes.
const (
	excelBPSQuotaBindingTTL = 10 * time.Minute
	excelBPSQuotaMaxDelay   = 24 * time.Hour
	excelBPSQuotaMaxEntries = 1024
)

// Only the provider's complete, typed rate-limit prefix can teach a relation.
// Arbitrary org-like text, request echoes, and other error classes cannot.
var excelBPSQuotaMessage = regexp.MustCompile("(?i)^rate limit reached for ([a-z0-9][a-z0-9._:-]{0,127}) in organization (org-[a-z0-9_-]{1,128}) on (tokens|requests) per min [(](TPM|RPM)[)]: limit [0-9]+(?:[.][0-9]+)?, used [0-9]+(?:[.][0-9]+)?, requested [0-9]+(?:[.][0-9]+)?[.]")

func excelBPSQuotaIdentity(failure excelBPSProviderFailure, errorType, message string) (string, string) {
	if failure.status != http.StatusTooManyRequests || !failure.retry || failure.permanent || failure.code != "rate_limit_exceeded" || len(message) > 4096 {
		return "", ""
	}
	match := excelBPSQuotaMessage.FindStringSubmatch(strings.TrimSpace(message))
	if len(match) != 5 || !strings.EqualFold(match[3], errorType) {
		return "", ""
	}
	if (strings.EqualFold(errorType, "tokens") && !strings.EqualFold(match[4], "TPM")) ||
		(strings.EqualFold(errorType, "requests") && !strings.EqualFold(match[4], "RPM")) {
		return "", ""
	}
	// Preserve the provider identity's case. Neither its text nor the complete
	// error message is retained by the feedback store or emitted to logs.
	sum := sha256.Sum256([]byte("excel-bps-provider-quota\x00" + match[2]))
	return hex.EncodeToString(sum[:]), match[1]
}

type excelBPSQuotaAccountKey struct {
	accountID int64
	model     string
}

type excelBPSQuotaScopeKey struct {
	groupHash string
	model     string
}

type excelBPSQuotaBinding struct {
	groupHash    string
	groupExpires time.Time
	until        time.Time
	expires      time.Time
}

// The zero value is ready to use. Embed one in the gateway service; sharing is
// process-local, and identities are learned separately for each account/model.
// Call only from the BPS route with its actual mapped upstream model. Account
// shadows may use RPMAccountID, but ordinary accounts never imply a group.
type excelBPSQuotaGate struct {
	mu       sync.Mutex
	accounts map[excelBPSQuotaAccountKey]excelBPSQuotaBinding
	scopes   map[excelBPSQuotaScopeKey]time.Time
}

func (g *excelBPSQuotaGate) observe(accountID int64, upstreamModel string, failure excelBPSProviderFailure, now time.Time) {
	model := strings.TrimSpace(upstreamModel)
	if g == nil || accountID <= 0 || model == "" || len(model) > 128 || failure.status != http.StatusTooManyRequests || !failure.retry || failure.permanent || failure.delay <= 0 || failure.delay > excelBPSQuotaMaxDelay {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.accounts == nil {
		g.accounts = make(map[excelBPSQuotaAccountKey]excelBPSQuotaBinding)
		g.scopes = make(map[excelBPSQuotaScopeKey]time.Time)
	}
	// Prune before insertion and refuse new identities at capacity. Existing
	// live cooldowns must not be evicted by a burst of unrelated identities.
	for key, binding := range g.accounts {
		if !now.Before(binding.expires) {
			delete(g.accounts, key)
		}
	}
	for key, until := range g.scopes {
		if !now.Before(until) {
			delete(g.scopes, key)
		}
	}
	key := excelBPSQuotaAccountKey{accountID: accountID, model: model}
	binding, exists := g.accounts[key]
	if !exists && len(g.accounts) >= excelBPSQuotaMaxEntries {
		return
	}
	until := now.Add(failure.delay)
	if until.After(binding.until) {
		binding.until = until
	}
	binding.expires = now.Add(excelBPSQuotaBindingTTL)
	if binding.until.After(binding.expires) {
		binding.expires = binding.until
	}
	// Account-only throttles cannot renew a learned provider relationship.
	if !now.Before(binding.groupExpires) {
		binding.groupHash = ""
		binding.groupExpires = time.Time{}
	}
	groupObserved := false
	if failure.quotaModel == model && len(failure.quotaGroupHash) == sha256.Size*2 {
		if _, err := hex.DecodeString(failure.quotaGroupHash); err == nil {
			binding.groupHash = failure.quotaGroupHash
			binding.groupExpires = now.Add(excelBPSQuotaBindingTTL)
			if until.After(binding.groupExpires) {
				binding.groupExpires = until
			}
			groupObserved = true
		}
	}
	g.accounts[key] = binding
	// A later untyped endpoint 429 only cools its account/model, even if we
	// previously learned a provider group. It cannot extend that group.
	if !groupObserved {
		return
	}
	scope := excelBPSQuotaScopeKey{groupHash: binding.groupHash, model: model}
	current, exists := g.scopes[scope]
	if !exists && len(g.scopes) >= excelBPSQuotaMaxEntries {
		return
	}
	if until.After(current) {
		g.scopes[scope] = until
	}
}

// delay does no sleeping and spends no send/RPM allowance. The caller must
// bound any wait by the same request recovery deadline, recheck after waking,
// and retain all hard scheduling and semantic-output guards.
func (g *excelBPSQuotaGate) delay(accountID int64, upstreamModel string, now time.Time) time.Duration {
	model := strings.TrimSpace(upstreamModel)
	if g == nil || accountID <= 0 || model == "" || len(model) > 128 {
		return 0
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	key := excelBPSQuotaAccountKey{accountID: accountID, model: model}
	binding, exists := g.accounts[key]
	if !exists {
		return 0
	}
	if !now.Before(binding.expires) {
		delete(g.accounts, key)
		return 0
	}
	until := binding.until
	if binding.groupHash != "" && !now.Before(binding.groupExpires) {
		binding.groupHash = ""
		binding.groupExpires = time.Time{}
		g.accounts[key] = binding
	}
	if binding.groupHash != "" {
		scope := excelBPSQuotaScopeKey{groupHash: binding.groupHash, model: model}
		if shared := g.scopes[scope]; now.Before(shared) {
			if shared.After(until) {
				until = shared
			}
		} else {
			delete(g.scopes, scope)
		}
	}
	if now.Before(until) {
		return until.Sub(now)
	}
	return 0
}
