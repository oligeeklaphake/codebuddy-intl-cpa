package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func authWithCPASettings(t *testing.T, flat bool, disabled bool) []byte {
	t.Helper()
	raw := sampleNestedAuthJSON("settings-test")
	if flat {
		raw = []byte(`{"type":"workbuddy","accessToken":"at","refreshToken":"rt","expiresAt":9999999999,"domain":"www.codebuddy.cn","uid":"settings-test","nickname":"nick"}`)
	}
	out, err := patchAuthJSON(raw, map[string]any{
		"excluded_models": []string{"claude-opus-4.6", "gpt-*"},
		"disabled":        disabled, "note": "CPA custom note", "prefix": "private",
		"proxy_url": "http://localhost:8080", "priority": 7,
		"headers":        map[string]string{"X-Custom": "keep"},
		"future_setting": json.RawMessage(`{"large_id":9007199254740993,"enabled":true}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func authDocument(t *testing.T, raw []byte) map[string]json.RawMessage {
	t.Helper()
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func assertAuthFieldsRetained(t *testing.T, before, after []byte, changed ...string) {
	t.Helper()
	oldDoc, newDoc := authDocument(t, before), authDocument(t, after)
	for _, key := range changed {
		delete(oldDoc, key)
		delete(newDoc, key)
	}
	if len(oldDoc) != len(newDoc) {
		t.Fatal("credential field set changed")
	}
	for key, oldRaw := range oldDoc {
		decode := func(raw []byte) any {
			t.Helper()
			decoder := json.NewDecoder(bytes.NewReader(raw))
			decoder.UseNumber()
			var value any
			if err := decoder.Decode(&value); err != nil {
				t.Fatal(err)
			}
			return value
		}
		// Compare values without treating key order as a change, retaining full
		// number precision so large unknown identifiers cannot be rounded.
		if !reflect.DeepEqual(decode(oldRaw), decode(newDoc[key])) {
			t.Fatalf("credential setting %s changed", key)
		}
	}
}

func mockAuthStore(t *testing.T, raw []byte, readErr error) *[]byte {
	t.Helper()
	original := authStoreCall
	t.Cleanup(func() { authStoreCall = original })
	var saved []byte
	authStoreCall = func(method string, request []byte) ([]byte, error) {
		switch method {
		case pluginabi.MethodHostAuthGet:
			if readErr != nil {
				return nil, readErr
			}
			return okEnvelope(rpcHostAuthGetResponse{
				AuthIndex: "settings-index", Name: "workbuddy-settings-test.json", JSON: raw,
			})
		case pluginabi.MethodHostAuthSave:
			var req pluginapi.HostAuthSaveRequest
			if err := json.Unmarshal(request, &req); err != nil {
				return nil, err
			}
			if req.Name != "workbuddy-settings-test.json" {
				t.Fatalf("unexpected target %q", req.Name)
			}
			saved = append([]byte(nil), req.JSON...)
			return okEnvelope(pluginapi.HostAuthSaveResponse{Name: req.Name})
		default:
			t.Fatalf("unexpected auth RPC %s", method)
			return nil, errors.New("unexpected RPC")
		}
	}
	return &saved
}

func TestLifecycleWritesRetainCPASettings(t *testing.T) {
	credits := &creditsSummary{TotalRemain: 10, TotalUsed: 1}
	cases := []struct {
		name                   string
		disabled, wantDisabled bool
		write                  func(*storedAuth) error
	}{
		{"note", true, true, func(sa *storedAuth) error {
			return syncAuthNote("settings-index", "settings-id", sa, credits, false)
		}},
		{"disable", false, true, func(sa *storedAuth) error {
			return disableAuth("settings-index", "settings-id", sa, credits, "test")
		}},
		{"reenable", true, false, func(sa *storedAuth) error {
			return reenableAuth("settings-index", "settings-id", sa, credits)
		}},
		{"delete-without-path", false, true, func(sa *storedAuth) error {
			return deleteAuth("settings-index", "settings-id", sa)
		}},
		{"session-dead", false, true, func(sa *storedAuth) error {
			return markSessionDead("settings-index", "settings-id", sa)
		}},
	}
	for _, flat := range []bool{false, true} {
		for _, tc := range cases {
			t.Run(tc.name+map[bool]string{true: "/flat", false: "/nested"}[flat], func(t *testing.T) {
				lifecycleState.Delete("settings-id")
				t.Cleanup(func() { lifecycleState.Delete("settings-id") })
				raw := authWithCPASettings(t, flat, tc.disabled)
				saved := mockAuthStore(t, raw, nil)
				sa, err := parseStored(raw)
				if err != nil {
					t.Fatal(err)
				}
				// A request may still hold tokens from before a concurrent refresh.
				sa.Auth.AccessToken = "stale-request-token"
				if err := tc.write(sa); err != nil {
					t.Fatal(err)
				}
				assertAuthFieldsRetained(t, raw, *saved, "note", "disabled")
				if parseDisabledFromAuthJSON(*saved) != tc.wantDisabled {
					t.Fatal("wrong disabled state")
				}
			})
		}
	}
}

func TestKeepaliveTokensRetainCPASettings(t *testing.T) {
	for _, flat := range []bool{false, true} {
		t.Run(map[bool]string{true: "flat", false: "nested"}[flat], func(t *testing.T) {
			raw := authWithCPASettings(t, flat, true)
			if !flat {
				nested, err := patchAuthJSON(authDocument(t, raw)["auth"], map[string]any{
					"scope": "offline_access", "future_token_setting": json.RawMessage(`9007199254740993`),
				})
				if err != nil {
					t.Fatal(err)
				}
				raw, err = patchAuthJSON(raw, map[string]any{"auth": json.RawMessage(nested)})
				if err != nil {
					t.Fatal(err)
				}
			}
			saved := mockAuthStore(t, raw, nil)
			sa, err := parseStored(raw)
			if err != nil {
				t.Fatal(err)
			}
			sa.Auth.AccessToken, sa.Auth.RefreshToken = "new-at", "new-rt"
			sa.Auth.ExpiresAt = 10000000000
			if err := persistAuthTokens("settings-index", sa); err != nil {
				t.Fatal(err)
			}
			if flat {
				assertAuthFieldsRetained(t, raw, *saved, "accessToken", "refreshToken", "expiresAt", "domain")
			} else {
				assertAuthFieldsRetained(t, raw, *saved, "auth")
				assertAuthFieldsRetained(t, authDocument(t, raw)["auth"], authDocument(t, *saved)["auth"],
					"accessToken", "refreshToken", "expiresAt", "domain")
			}
			updated, err := parseStored(*saved)
			if err != nil {
				t.Fatal(err)
			}
			if updated.Auth != sa.Auth {
				t.Fatal("refreshed tokens were not saved")
			}
		})
	}
}

func TestAuthWritesStopWhenCurrentFileCannotBeRead(t *testing.T) {
	sa, err := parseStored(sampleNestedAuthJSON("settings-test"))
	if err != nil {
		t.Fatal(err)
	}
	credits := &creditsSummary{TotalRemain: 10, TotalUsed: 1}
	writes := []func() error{
		func() error { return syncAuthNote("settings-index", "settings-id", sa, credits, false) },
		func() error { return disableAuth("settings-index", "settings-id", sa, credits, "test") },
		func() error { return reenableAuth("settings-index", "settings-id", sa, credits) },
		func() error { return persistAuthTokens("settings-index", sa) },
	}
	for _, raw := range [][]byte{nil, []byte(`{"auth":`), []byte(`null`)} {
		var readErr error
		if raw == nil {
			readErr = errors.New("auth store unavailable")
		}
		saved := mockAuthStore(t, raw, readErr)
		for _, write := range writes {
			lifecycleState.Delete("settings-id")
			if err := write(); err == nil {
				t.Fatal("expected read/decode error")
			}
			if len(*saved) != 0 {
				t.Fatal("saved partial auth after read/decode failure")
			}
		}
	}
}

func TestParseAndOAuthRefreshRetainCPASettings(t *testing.T) {
	for _, flat := range []bool{false, true} {
		t.Run(map[bool]string{true: "flat", false: "nested"}[flat], func(t *testing.T) {
			raw := authWithCPASettings(t, flat, true)
			// CPA decodes RPC metadata into map[string]any (float64 numbers), so
			// use a representable identifier in the host round-trip fixture.
			var err error
			raw, err = patchAuthJSON(raw, map[string]any{"future_setting": map[string]any{"id": 123, "enabled": true}})
			if err != nil {
				t.Fatal(err)
			}
			body, _ := json.Marshal(pluginapi.AuthParseRequest{
				Provider: providerName, FileName: "workbuddy-settings-test.json", RawJSON: raw,
			})
			out, err := handleParseAuth(body)
			if err != nil {
				t.Fatal(err)
			}
			parsed := decodeParseAuth(t, out).Auth
			assertAuthFieldsRetained(t, raw, parsed.StorageJSON)
			if !parsed.Disabled || parsed.Prefix != "private" || parsed.ProxyURL != "http://localhost:8080" {
				t.Fatal("parsed credential lost routing or disabled settings")
			}
			sa, err := parseStored(parsed.StorageJSON)
			if err != nil {
				t.Fatal(err)
			}
			sa.Auth.AccessToken, sa.Auth.RefreshToken = "new-at", "new-rt"
			// Simulate edits in CPA metadata, including stale credential metadata.
			metadata := parsed.Metadata
			metadata["excluded_models"] = []string{"new-exclusion"}
			metadata["auth"] = json.RawMessage(authDocument(t, sampleNestedAuthJSON("settings-test"))["auth"])
			if flat {
				delete(metadata, "auth")
				metadata["accessToken"] = "stale-at"
			}
			attrs := map[string]string{"excluded_models": "new-exclusion", "source": "original"}
			refreshed, err := toAuthDataForRefresh(sa, pluginapi.AuthRefreshRequest{
				StorageJSON: parsed.StorageJSON, Metadata: metadata, Attributes: attrs,
			})
			if err != nil {
				t.Fatal(err)
			}
			// Reproduce CPA's persistence overlay: Metadata wins over StorageJSON.
			persisted, err := patchAuthJSON(refreshed.StorageJSON, refreshed.Metadata)
			if err != nil {
				t.Fatal(err)
			}
			updated, err := parseStored(persisted)
			if err != nil {
				t.Fatal(err)
			}
			if updated.Auth != sa.Auth {
				t.Fatal("metadata restored stale tokens")
			}
			if !refreshed.Disabled || refreshed.FileName != "" || refreshed.ID != "" || !reflect.DeepEqual(refreshed.Attributes, attrs) {
				t.Fatal("refresh reset disabled state, identity, or attributes")
			}
			var excluded []string
			if err := json.Unmarshal(authDocument(t, persisted)["excluded_models"], &excluded); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(excluded, []string{"new-exclusion"}) {
				t.Fatal("lost host metadata edit")
			}
			changed := []string{"excluded_models", "auth", "provider", "logo"}
			if flat {
				changed = []string{"excluded_models", "accessToken", "refreshToken", "expiresAt", "domain", "provider", "logo"}
			}
			assertAuthFieldsRetained(t, raw, persisted, changed...)
		})
	}
}
