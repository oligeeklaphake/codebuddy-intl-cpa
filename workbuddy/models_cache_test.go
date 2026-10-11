package main

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type modelTestTransport func(*http.Request) (*http.Response, error)

func (f modelTestTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func resetModelCacheForTest(t *testing.T) {
	t.Helper()
	dynamicModelsCache.Lock()
	original := dynamicModelsCache.entries
	dynamicModelsCache.entries = nil
	dynamicModelsCache.Unlock()
	t.Cleanup(func() {
		dynamicModelsCache.Lock()
		dynamicModelsCache.entries = original
		dynamicModelsCache.Unlock()
	})
}

func useModelTransport(t *testing.T, transport modelTestTransport) {
	t.Helper()
	client := sharedHTTPClient()
	original := client.Transport
	client.Transport = transport
	t.Cleanup(func() { client.Transport = original })
}

func modelTestResponse(ids ...string) *http.Response {
	models := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		models = append(models, map[string]any{"id": id, "name": id, "contextWindow": 131072})
	}
	body, _ := json.Marshal(map[string]any{
		"code": 0, "data": map[string]any{
			"models": models, "agents": []map[string]any{{"name": "cli", "models": ids}},
		},
	})
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(body)))}
}

func modelStorage(t *testing.T, domain, uid, enterprise, token string, flat bool) []byte {
	t.Helper()
	var doc any = &storedAuth{
		Auth:    storedTokens{AccessToken: token, Domain: domain},
		Account: storedAccount{UID: uid, EnterpriseID: enterprise},
	}
	if flat {
		doc = map[string]any{"accessToken": token, "domain": domain, "uid": uid, "enterpriseId": enterprise}
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func authModelIDs(t *testing.T, storage []byte, excluded ...string) []string {
	t.Helper()
	body, err := json.Marshal(pluginapi.AuthModelRequest{
		AuthProvider: providerName, StorageJSON: storage,
		Host: pluginapi.HostConfigSummary{ExcludedModels: map[string][]string{providerName: excluded}},
	})
	if err != nil {
		t.Fatal(err)
	}
	out, err := handleModelForAuth(body)
	if err != nil {
		t.Fatal(err)
	}
	var env envelope
	if err := json.Unmarshal(out, &env); err != nil || !env.OK {
		t.Fatal("invalid model envelope")
	}
	var resp pluginapi.ModelResponse
	if err := json.Unmarshal(env.Result, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Provider != providerName {
		t.Fatal("incorrect model provider")
	}
	ids := make([]string, len(resp.Models))
	for i, model := range resp.Models {
		ids[i] = model.ID
	}
	return ids
}

func TestModelListsIsolatedAcrossRealmsAndAccounts(t *testing.T) {
	for _, globalFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "CN-first", true: "Global-first"}[globalFirst], func(t *testing.T) {
			resetModelCacheForTest(t)
			calls := make(map[string]int)
			useModelTransport(t, func(req *http.Request) (*http.Response, error) {
				token := strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer ")
				calls[token]++
				wantHost, wantOrigin := "copilot.tencent.com", originReferer
				if strings.HasPrefix(token, "global") {
					wantHost, wantOrigin = "www.codebuddy.ai", originRefererGlobal
				}
				if req.URL.Host != wantHost || req.Header.Get("Origin") != wantOrigin || req.Header.Get("Referer") != wantOrigin+"/" {
					t.Fatal("model request used the wrong realm")
				}
				return modelTestResponse(token+"-model", "shared-model"), nil
			})
			cn := modelStorage(t, "www.codebuddy.cn", "same-uid", "", "cn-1", false)
			global := modelStorage(t, "www.workbuddy.ai", "same-uid", "", "global-1", true)
			order := [][]byte{cn, global}
			wantOrder := [][]string{{"cn-1-model", "shared-model"}, {"global-1-model", "shared-model"}}
			if globalFirst {
				order[0], order[1] = order[1], order[0]
				wantOrder[0], wantOrder[1] = wantOrder[1], wantOrder[0]
			}
			for pass := 0; pass < 2; pass++ {
				for i, storage := range order {
					if got := authModelIDs(t, storage); !reflect.DeepEqual(got, wantOrder[i]) {
						t.Fatalf("wrong models: %v", got)
					}
				}
			}
			otherGlobal := modelStorage(t, "www.codebuddy.ai", "other-uid", "", "global-2", false)
			if got := authModelIDs(t, otherGlobal); !reflect.DeepEqual(got, []string{"global-2-model", "shared-model"}) {
				t.Fatal("accounts in the same realm shared models")
			}
			// A filter for one call must not modify the cached complete list.
			if got := authModelIDs(t, cn, "shared-model"); !reflect.DeepEqual(got, []string{"cn-1-model"}) {
				t.Fatal("exclusion was not applied")
			}
			if got := authModelIDs(t, cn); !reflect.DeepEqual(got, []string{"cn-1-model", "shared-model"}) {
				t.Fatal("filter corrupted cache")
			}
			if !reflect.DeepEqual(calls, map[string]int{"cn-1": 1, "global-1": 1, "global-2": 1}) {
				t.Fatalf("unexpected API calls: %v", calls)
			}
		})
	}
}

func TestModelCacheIdentityAndExpiry(t *testing.T) {
	resetModelCacheForTest(t)
	var calls int
	useModelTransport(t, func(req *http.Request) (*http.Response, error) {
		calls++
		return modelTestResponse(strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer ")), nil
	})
	fixtures := [][]byte{
		modelStorage(t, "www.codebuddy.cn", "uid", "enterprise-a", "token", false),
		modelStorage(t, "www.codebuddy.cn", "uid", "enterprise-b", "token", false),
		modelStorage(t, "www.codebuddy.cn", "other-uid", "enterprise-a", "token", false),
		modelStorage(t, "www.codebuddy.cn", "uid", "enterprise-a", "renewed-token", false),
		modelStorage(t, "www.codebuddy.cn", "", "", "legacy-token-a", true),
		modelStorage(t, "www.codebuddy.cn", "", "", "legacy-token-b", true),
	}
	for pass := 0; pass < 2; pass++ {
		for _, storage := range fixtures {
			authModelIDs(t, storage)
		}
	}
	if calls != len(fixtures) {
		t.Fatal("cache was not separated by credential identity")
	}
	sa, err := parseStored(fixtures[0])
	if err != nil {
		t.Fatal(err)
	}
	key := modelCacheKey(sa)
	dynamicModelsCache.Lock()
	entry := dynamicModelsCache.entries[key]
	entry.fetched = time.Now().Add(-dynamicModelsCacheTTL - time.Second)
	dynamicModelsCache.entries[key] = entry
	dynamicModelsCache.Unlock()
	authModelIDs(t, fixtures[0])
	if calls != len(fixtures)+1 {
		t.Fatal("expired model list was reused")
	}
}

func TestLegacyModelRealmUsesJWTOnlyWhenDomainMissing(t *testing.T) {
	resetModelCacheForTest(t)
	globalToken := "header." + base64.RawURLEncoding.EncodeToString([]byte(`{"iss":"https://www.workbuddy.ai/realms/workbuddy"}`)) + ".signature"
	var hosts []string
	useModelTransport(t, func(req *http.Request) (*http.Response, error) {
		hosts = append(hosts, req.URL.Host)
		return modelTestResponse("discovered"), nil
	})
	authModelIDs(t, modelStorage(t, "", "legacy-global", "", globalToken, false))
	authModelIDs(t, modelStorage(t, "www.codebuddy.cn", "declared-cn", "", globalToken, true))
	if !reflect.DeepEqual(hosts, []string{"www.codebuddy.ai", "copilot.tencent.com"}) {
		t.Fatalf("incorrect realm routing: %v", hosts)
	}
}

func TestModelDiscoveryFailureCannotBorrowOrPoisonOtherAuth(t *testing.T) {
	resetModelCacheForTest(t)
	var failedCalls int
	useModelTransport(t, func(req *http.Request) (*http.Response, error) {
		if req.Header.Get("Authorization") == "Bearer fail-once" {
			failedCalls++
			if failedCalls == 1 {
				return &http.Response{StatusCode: 500, Body: io.NopCloser(strings.NewReader("unavailable"))}, nil
			}
			return modelTestResponse("recovered-cn"), nil
		}
		return modelTestResponse("global-only"), nil
	})
	authModelIDs(t, modelStorage(t, "www.workbuddy.ai", "global", "", "global", false))
	cn := modelStorage(t, "www.codebuddy.cn", "cn", "", "fail-once", false)
	for _, raw := range [][]byte{cn, nil, []byte(`{"auth":{}}`), []byte(`invalid`)} {
		got := authModelIDs(t, raw)
		for _, id := range got {
			if id == "global-only" {
				t.Fatal("failed auth inherited another realm's cache")
			}
		}
		if len(got) != len(wbModels()) {
			t.Fatal("expected existing static fallback")
		}
	}
	if got := authModelIDs(t, cn); !reflect.DeepEqual(got, []string{"recovered-cn"}) {
		t.Fatal("failure poisoned the auth cache")
	}
}

func TestConcurrentModelDiscoveryIsolatedAndDeduplicated(t *testing.T) {
	resetModelCacheForTest(t)
	var mu sync.Mutex
	calls := make(map[string]int)
	useModelTransport(t, func(req *http.Request) (*http.Response, error) {
		token := strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer ")
		mu.Lock()
		calls[token]++
		mu.Unlock()
		return modelTestResponse(token), nil
	})
	cn := modelStorage(t, "www.codebuddy.cn", "cn", "", "cn-only", false)
	global := modelStorage(t, "www.workbuddy.ai", "global", "", "global-only", false)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			storage, want := cn, "cn-only"
			if i%2 == 1 {
				storage, want = global, "global-only"
			}
			got := fetchDynamicModelsFromStorage(storage)
			if len(got) != 1 || got[0].ID != want {
				t.Error("concurrent calls shared another auth's result")
			}
		}(i)
	}
	wg.Wait()
	if !reflect.DeepEqual(calls, map[string]int{"cn-only": 1, "global-only": 1}) {
		t.Fatalf("concurrent API calls were not deduplicated: %v", calls)
	}
}
