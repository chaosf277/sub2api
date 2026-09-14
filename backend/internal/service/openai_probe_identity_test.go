//go:build unit

package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOpenAIProbeIdentityAcrossPaths(t *testing.T) {
	for _, mode := range []string{"off", "device", "session", "full"} {
		for _, path := range []string{"test", "compact", "usage"} {
			t.Run(path+"/"+mode, func(t *testing.T) {
				account := &Account{ID: 81, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
					Credentials: map[string]any{"access_token": "fixture-token", "chatgpt_account_id": "fixture-account"},
					Extra:       map[string]any{"codex_fingerprint_mode": mode, "codex_fingerprint_seed": "11111111-1111-4111-8111-111111111111"},
				}
				resp := newJSONResponse(http.StatusOK, compactProbeSSESuccessBody)
				resp.Header.Set("x-codex-primary-used-percent", "42")
				resp.Header.Set("x-codex-primary-window-minutes", "300")
				upstream := &queuedHTTPUpstream{responses: []*http.Response{resp}}
				if path == "usage" {
					svc := &AccountUsageService{httpUpstream: upstream}
					updates, err := svc.probeOpenAICodexSnapshot(context.Background(), account)
					require.NoError(t, err)
					require.Equal(t, 42.0, updates["codex_5h_used_percent"])
					require.Len(t, upstream.requests, 1)
					deadline, ok := upstream.requests[0].Context().Deadline()
					require.True(t, ok)
					require.WithinDuration(t, time.Now().Add(15*time.Second), deadline, time.Second)
				} else {
					svc := &AccountTestService{httpUpstream: upstream}
					ctx, _ := newTestContext()
					testMode := AccountTestModeDefault
					if path == "compact" {
						testMode = AccountTestModeCompact
					}
					require.NoError(t, svc.testOpenAIAccountConnection(ctx, account, "gpt-5.4", "", testMode))
				}
				require.Len(t, upstream.requests, 1)
				// OpenAI uses the shared TLS-capable transport with no Anthropic-only profile.
				require.Equal(t, []bool{false}, upstream.tlsFlags)
				req := upstream.requests[0]
				require.Equal(t, HTTPUpstreamProfileOpenAI, HTTPUpstreamProfileFromContext(req.Context()))
				h := req.Header
				require.NotEmpty(t, h.Get("session-id"))
				require.Equal(t, h.Get("session-id"), h.Get("session_id"))
				require.NotEmpty(t, h.Get("thread-id"))
				require.Equal(t, h.Get("thread-id"), h.Get("x-client-request-id"))
				if path == "compact" {
					require.Equal(t, h.Get("session-id"), h.Get("conversation_id"))
				}
				if mode == "off" {
					require.Empty(t, h.Get("x-codex-installation-id"))
				} else {
					require.NotEmpty(t, h.Get("x-codex-installation-id"))
				}
			})
		}
	}
}

func TestOpenAIUsageProbePluginFailureDoesNotFallBack(t *testing.T) {
	manager := &PluginManager{}
	manager.route.Store(&pluginRoute{pluginID: 1, rolloutPercent: 100, unavailable: "fixture unavailable"})
	upstream := &queuedHTTPUpstream{}
	svc := &AccountUsageService{httpUpstream: upstream, pluginManager: manager}
	_, err := svc.probeOpenAICodexSnapshot(context.Background(), &Account{
		ID: 81, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Credentials: map[string]any{"access_token": "fixture-token"},
	})
	require.ErrorContains(t, err, "插件不可用")
	require.Empty(t, upstream.requests)
}

func TestOpenAIProbeIdentityScopesCredentialParent(t *testing.T) {
	selected := &Account{ID: 9, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	var sessions []string
	for _, id := range []string{"parent-a", "parent-b", "parent-a"} {
		parent := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"chatgpt_account_id": id}}
		h := http.Header{}
		h.Set("originator", "codex_cli_rs")
		h.Set("session-id", "same-session")
		h.Set("thread-id", "same-thread")
		h.Set("conversation_id", "conflicting-conversation")
		h.Set("x-client-request-id", "conflicting-request")
		applyOpenAICodexProbeIdentity(h, selected, parent)
		require.Equal(t, h.Get("session-id"), h.Get("conversation_id"))
		require.Equal(t, h.Get("thread-id"), h.Get("x-client-request-id"))
		sessions = append(sessions, h.Get("session-id"))
	}
	require.NotEqual(t, sessions[0], sessions[1])
	require.Equal(t, sessions[0], sessions[2])
}
