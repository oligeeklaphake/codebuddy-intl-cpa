// models.go implements the ModelProvider capability: static and per-auth
// model lists, dynamic model discovery via the upstream models API, alias
// reverse resolution (client-facing alias → upstream model id), and the
// host-config oauth-excluded-models filter.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func wbModels() []pluginapi.ModelInfo {
	return []pluginapi.ModelInfo{
		{ID: "glm-5.2", Name: "GLM-5.2", ContextLength: 1000000, MaxCompletionTokens: 8192, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
		{ID: "glm-5.1", Name: "GLM-5.1", ContextLength: 131072, MaxCompletionTokens: 8192, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
		{ID: "glm-5v-turbo", Name: "GLM-5V Turbo", ContextLength: 131072, MaxCompletionTokens: 8192, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
		{ID: "kimi-k2.7", Name: "Kimi K2.7", ContextLength: 262144, MaxCompletionTokens: 8192, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
		{ID: "kimi-k3", Name: "Kimi K3", ContextLength: 262144, MaxCompletionTokens: 8192, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
		{ID: "minimax-m3", Name: "MiniMax M3", ContextLength: 204800, MaxCompletionTokens: 8192, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
		{ID: "hy3", Name: "Hy3", ContextLength: 262144, MaxCompletionTokens: 8192, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
		{ID: "hy3-preview", Name: "Hy3 Preview", ContextLength: 262144, MaxCompletionTokens: 8192, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
		{ID: "hy3-preview-agent", Name: "Hy3 Preview Agent", ContextLength: 262144, MaxCompletionTokens: 8192, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
		{ID: "deepseek-v4-pro", Name: "DeepSeek V4 Pro", ContextLength: 1000000, MaxCompletionTokens: 8192, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
		{ID: "deepseek-v4-flash", Name: "DeepSeek V4 Flash", ContextLength: 1000000, MaxCompletionTokens: 8192, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
		// Hidden models that the CodeBuddy upstream accepts when called directly.
		{ID: "gpt-5.6-luna", Name: "GPT-5.6 Luna", ContextLength: 200000, MaxCompletionTokens: 16384, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
		{ID: "claude-opus-5", Name: "Claude Opus 5", ContextLength: 200000, MaxCompletionTokens: 16384, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
		{ID: "gpt-5.6-sol", Name: "GPT-5.6 Sol", ContextLength: 200000, MaxCompletionTokens: 16384, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
		{ID: "glm-5.3", Name: "GLM-5.3", ContextLength: 200000, MaxCompletionTokens: 16384, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
		{ID: "gpt-5.6-terra", Name: "GPT-5.6 Terra", ContextLength: 200000, MaxCompletionTokens: 16384, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
		{ID: "deepseek-v4.1-flash", Name: "DeepSeek V4.1 Flash", ContextLength: 200000, MaxCompletionTokens: 16384, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
		{ID: "glm-5.3-flash", Name: "GLM-5.3 Flash", ContextLength: 200000, MaxCompletionTokens: 16384, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
		// Live-verified 2026-09-29: 200 on a Global account, served as
		// gpt-6-astra, credit 0.04 for 18 prompt + 5 completion tokens. Not in
		// any other CPA plugin's list, so it is easy to miss.
		{ID: "gpt-6-astra", Name: "GPT-6 Astra", ContextLength: 200000, MaxCompletionTokens: 16384, OwnedBy: providerName, SupportedGenerationMethods: []string{"chat"}},
	}
}

type dynamicModelCacheKey struct {
	gateway      string
	uid          string
	enterpriseID string
	tokenHash    [32]byte
}

type dynamicModelCacheEntry struct {
	models  []pluginapi.ModelInfo
	fetched time.Time
}

type dynamicModelCall struct {
	done   chan struct{}
	models []pluginapi.ModelInfo
}

var dynamicModelFlights sync.Map // dynamicModelCacheKey -> *dynamicModelCall

func modelCacheKey(sa *storedAuth) dynamicModelCacheKey {
	return dynamicModelCacheKey{
		gateway: endpointModelsFor(sa), uid: sa.Account.UID,
		enterpriseID: sa.Account.EnterpriseID,
		tokenHash:    sha256.Sum256([]byte(sa.Auth.AccessToken)),
	}
}

func cachedDynamicModels(key dynamicModelCacheKey) ([]pluginapi.ModelInfo, bool) {
	dynamicModelsCache.RLock()
	defer dynamicModelsCache.RUnlock()
	entry, ok := dynamicModelsCache.entries[key]
	if ok && len(entry.models) > 0 && time.Since(entry.fetched) < dynamicModelsCacheTTL {
		return append([]pluginapi.ModelInfo(nil), entry.models...), true
	}
	return nil, false
}

func storeDynamicModels(key dynamicModelCacheKey, models []pluginapi.ModelInfo) {
	dynamicModelsCache.Lock()
	defer dynamicModelsCache.Unlock()
	if dynamicModelsCache.entries == nil {
		dynamicModelsCache.entries = make(map[dynamicModelCacheKey]dynamicModelCacheEntry)
	}
	now := time.Now()
	// Expire obsolete token entries and bound memory under account/token churn.
	for cachedKey, entry := range dynamicModelsCache.entries {
		if now.Sub(entry.fetched) >= dynamicModelsCacheTTL {
			delete(dynamicModelsCache.entries, cachedKey)
		}
	}
	if _, exists := dynamicModelsCache.entries[key]; !exists && len(dynamicModelsCache.entries) >= 256 {
		var oldestKey dynamicModelCacheKey
		var oldest time.Time
		for cachedKey, entry := range dynamicModelsCache.entries {
			if oldest.IsZero() || entry.fetched.Before(oldest) {
				oldestKey, oldest = cachedKey, entry.fetched
			}
		}
		delete(dynamicModelsCache.entries, oldestKey)
	}
	dynamicModelsCache.entries[key] = dynamicModelCacheEntry{
		models: append([]pluginapi.ModelInfo(nil), models...), fetched: now,
	}
}

func fetchDynamicModelsFromStorage(storageJSON []byte) (models []pluginapi.ModelInfo) {
	// Resolve this credential before looking up the cache. A token-less or
	// malformed credential must never inherit another account's model list.
	sa, err := parseStored(storageJSON)
	if err != nil {
		return wbModels()
	}
	// Legacy files may omit domain. Use the JWT issuer only as a fallback;
	// opaque tokens still route correctly when the credential declares domain.
	if strings.TrimSpace(sa.Auth.Domain) == "" && isGlobalToken(sa.Auth.AccessToken) {
		sa.Auth.Domain = "www.codebuddy.ai"
	}
	key := modelCacheKey(sa)
	if cached, ok := cachedDynamicModels(key); ok {
		return cached
	}
	call := &dynamicModelCall{done: make(chan struct{})}
	actual, loaded := dynamicModelFlights.LoadOrStore(key, call)
	if loaded {
		other := actual.(*dynamicModelCall)
		<-other.done
		return append([]pluginapi.ModelInfo(nil), other.models...)
	}
	defer func() {
		call.models = models
		close(call.done)
		dynamicModelFlights.Delete(key)
	}()
	if cached, ok := cachedDynamicModels(key); ok {
		return cached
	}
	if dyn, err := callModelsAPI(sa); err == nil && len(dyn) > 0 {
		storeDynamicModels(key, dyn)
		return dyn
	}
	return wbModels()
}

// isGlobalToken decodes the JWT iss claim to determine the account realm.
// Global tokens have iss=...workbuddy.ai...; CN tokens have iss=...codebuddy.cn...
// Returns true if the token is Global.
func isGlobalToken(accessToken string) bool {
	parts := strings.Split(accessToken, ".")
	if len(parts) < 2 {
		return false
	}
	payload := parts[1]
	// base64url padding
	if pad := len(payload) % 4; pad != 0 {
		payload += strings.Repeat("=", 4-pad)
	}
	raw, err := base64.URLEncoding.DecodeString(payload)
	if err != nil {
		return false
	}
	var claims struct {
		ISS string `json:"iss"`
	}
	if json.Unmarshal(raw, &claims) != nil {
		return false
	}
	return strings.Contains(strings.ToLower(claims.ISS), "workbuddy.ai") || strings.Contains(strings.ToLower(claims.ISS), "codebuddy.ai")
}

// callModelsAPI GETs /console/enterprises/personal/models from the upstream.
// Uses the shared client (connection pooling) with a per-request 15s budget;
// the shared client's own 120s timeout stays as the outer bound.
func callModelsAPI(sa *storedAuth) ([]pluginapi.ModelInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	modelsURL := endpointModelsFor(sa)
	origin := originRefererFor(sa)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, modelsURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+sa.Auth.AccessToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Origin", origin)
	req.Header.Set("Referer", origin+"/")
	req.Header.Set("User-Agent", clientUA)
	resp, err := hostHTTPDo(req)
	if err != nil {
		return nil, err
	}
	body := resp.Body
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("models API status %d", resp.StatusCode)
	}
	var apiResp struct {
		Code int `json:"code"`
		Data struct {
			Models []struct {
				ID                 string          `json:"id"`
				Name               string          `json:"name"`
				Description        string          `json:"description"`
				Credits            string          `json:"credits"`
				Configurable       bool            `json:"configurable"`
				Configured         bool            `json:"configured"`
				IsDefault          bool            `json:"isDefault"`
				SupportsImages     bool            `json:"supportsImages"`
				SupportsReasoning  bool            `json:"supportsReasoning"`
				OnlyReasoning      bool            `json:"onlyReasoning"`
				Reasoning          json.RawMessage `json:"reasoning"`
				DisabledMultimodal bool            `json:"disabledMultimodal"`
				Disabled           bool            `json:"disabled"`
				DisabledReason     string          `json:"disabledReason"`
				ContextWindow      json.RawMessage `json:"contextWindow"`
				MaxTokens          json.RawMessage `json:"maxTokens"`
			} `json:"models"`
			Agents []struct {
				Name   string   `json:"name"`
				Models []string `json:"models"`
			} `json:"agents"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return nil, err
	}
	if apiResp.Code != 0 {
		return nil, fmt.Errorf("models API code %d", apiResp.Code)
	}
	var cliModelIDs []string
	for _, a := range apiResp.Data.Agents {
		if a.Name == "cli" {
			cliModelIDs = a.Models
			break
		}
	}
	if len(cliModelIDs) == 0 {
		return nil, fmt.Errorf("no cli agent models found")
	}
	dynMap := make(map[string]struct {
		ID                 string          `json:"id"`
		Name               string          `json:"name"`
		Description        string          `json:"description"`
		Credits            string          `json:"credits"`
		Configurable       bool            `json:"configurable"`
		Configured         bool            `json:"configured"`
		IsDefault          bool            `json:"isDefault"`
		SupportsImages     bool            `json:"supportsImages"`
		SupportsReasoning  bool            `json:"supportsReasoning"`
		OnlyReasoning      bool            `json:"onlyReasoning"`
		Reasoning          json.RawMessage `json:"reasoning"`
		DisabledMultimodal bool            `json:"disabledMultimodal"`
		Disabled           bool            `json:"disabled"`
		DisabledReason     string          `json:"disabledReason"`
		ContextWindow      json.RawMessage `json:"contextWindow"`
		MaxTokens          json.RawMessage `json:"maxTokens"`
	}, len(apiResp.Data.Models))
	for _, m := range apiResp.Data.Models {
		dynMap[m.ID] = m
	}
	var out []pluginapi.ModelInfo
	for _, id := range cliModelIDs {
		m, ok := dynMap[id]
		if !ok {
			continue
		}
		if m.Disabled {
			continue
		}
		ctxLen := int64(0)
		if len(m.ContextWindow) > 0 {
			var v float64
			if err := json.Unmarshal(m.ContextWindow, &v); err == nil {
				ctxLen = int64(v)
			}
		}
		maxTok := int64(0)
		if len(m.MaxTokens) > 0 {
			var v float64
			if err := json.Unmarshal(m.MaxTokens, &v); err == nil {
				maxTok = int64(v)
			}
		}
		out = append(out, pluginapi.ModelInfo{
			ID:                         m.ID,
			Name:                       m.Name,
			ContextLength:              ctxLen,
			MaxCompletionTokens:        maxTok,
			OwnedBy:                    providerName,
			SupportedGenerationMethods: []string{"chat"},
		})
	}
	return out, nil
}

func cacheModelAliases(host pluginapi.HostConfigSummary) {
	entries := host.OAuthModelAlias[providerName]
	if len(entries) == 0 {
		// Host may key the channel case-insensitively; fall back to a scan.
		for channel, list := range host.OAuthModelAlias {
			if strings.EqualFold(strings.TrimSpace(channel), providerName) {
				entries = list
				break
			}
		}
	}
	byAlias := make(map[string]string, len(entries))
	for _, e := range entries {
		name := strings.TrimSpace(e.Name)
		alias := strings.TrimSpace(e.Alias)
		if name == "" || alias == "" || strings.EqualFold(name, alias) {
			continue
		}
		byAlias[strings.ToLower(alias)] = name
	}
	modelAliasCache.Lock()
	modelAliasCache.byAlias = byAlias
	modelAliasCache.Unlock()
}

// resolveUpstreamModel maps an aliased requested model back to the real
// upstream model ID. Returns the input unchanged when nothing matches.
func resolveUpstreamModel(model string, attributes map[string]string) string {
	m := strings.TrimSpace(model)
	if m == "" {
		return model
	}
	key := strings.ToLower(m)
	if name, ok := parseModelAliasAttribute(attributes)[key]; ok {
		return name
	}
	modelAliasCache.RLock()
	name, ok := modelAliasCache.byAlias[key]
	modelAliasCache.RUnlock()
	if ok {
		return name
	}
	return m
}

// parseModelAliasAttribute decodes a per-auth alias override from auth
// attributes. Accepts JSON ([{"name":...,"alias":...}] or {alias:name}) or
// comma-separated "alias=name" pairs.
func parseModelAliasAttribute(attributes map[string]string) map[string]string {
	if len(attributes) == 0 {
		return nil
	}
	raw := ""
	for _, k := range []string{"model_alias", "model-alias", "oauth-model-alias"} {
		if v := strings.TrimSpace(attributes[k]); v != "" {
			raw = v
			break
		}
	}
	if raw == "" {
		return nil
	}
	out := make(map[string]string)
	add := func(name, alias string) {
		name, alias = strings.TrimSpace(name), strings.TrimSpace(alias)
		if name != "" && alias != "" && !strings.EqualFold(name, alias) {
			out[strings.ToLower(alias)] = name
		}
	}
	if strings.HasPrefix(raw, "[") {
		var list []struct {
			Name  string `json:"name"`
			Alias string `json:"alias"`
		}
		if json.Unmarshal([]byte(raw), &list) == nil {
			for _, e := range list {
				add(e.Name, e.Alias)
			}
			return out
		}
	}
	if strings.HasPrefix(raw, "{") {
		var m map[string]string
		if json.Unmarshal([]byte(raw), &m) == nil {
			for alias, name := range m {
				add(name, alias)
			}
			return out
		}
	}
	for _, pair := range strings.Split(raw, ",") {
		kv := strings.SplitN(pair, "=", 2)
		if len(kv) == 2 {
			add(kv[1], kv[0])
		}
	}
	return out
}

// filterExcludedModels removes models listed in oauth-excluded-models for
// the workbuddy provider. The host passes this config via HostConfigSummary.
func filterExcludedModels(models []pluginapi.ModelInfo, host pluginapi.HostConfigSummary) []pluginapi.ModelInfo {
	if len(host.ExcludedModels) == 0 {
		return models
	}
	// Try exact provider match, then case-insensitive scan.
	excluded := host.ExcludedModels[providerName]
	if len(excluded) == 0 {
		for channel, list := range host.ExcludedModels {
			if strings.EqualFold(strings.TrimSpace(channel), providerName) {
				excluded = list
				break
			}
		}
	}
	if len(excluded) == 0 {
		return models
	}
	excludeSet := make(map[string]struct{}, len(excluded))
	for _, m := range excluded {
		excludeSet[strings.ToLower(strings.TrimSpace(m))] = struct{}{}
	}
	// Use a fresh slice — models[:0] would alias the input's backing array,
	// which may be the dynamicModelsCache's own slice. Mutating it in place
	// would corrupt the cache for subsequent callers (P0 bug: after one
	// filterExcludedModels call, cache returns the filtered list as the
	// "full" list on the next fetch).
	out := make([]pluginapi.ModelInfo, 0, len(models))
	for _, m := range models {
		if _, skip := excludeSet[strings.ToLower(m.ID)]; skip {
			continue
		}
		out = append(out, m)
	}
	return out
}

// publishUsage reports one upstream attempt into CPAMP request monitoring.
// requestedModel is client-facing (may be alias); upstreamModel is resolved.

func handleModelStatic(raw []byte) ([]byte, error) {
	var req pluginapi.StaticModelRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	cacheModelAliases(req.Host)
	models := wbModels()
	models = filterExcludedModels(models, req.Host)
	return okEnvelope(pluginapi.ModelResponse{Provider: providerName, Models: models})
}

func handleModelForAuth(raw []byte) ([]byte, error) {
	var req pluginapi.AuthModelRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	// Always return the plugin's canonical provider key. The host skips any
	// response whose Provider doesn't match the auth's provider, so echoing
	// req.AuthProvider back would silently drop the model list whenever the
	// auth file carries a non-canonical provider string.
	cacheModelAliases(req.Host)
	models := fetchDynamicModelsFromStorage(req.StorageJSON)
	models = filterExcludedModels(models, req.Host)
	return okEnvelope(pluginapi.ModelResponse{Provider: providerName, Models: models})
}
