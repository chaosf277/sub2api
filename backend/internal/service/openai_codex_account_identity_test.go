package service

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type codexAccountIdentityRepoStub struct {
	AccountRepository
	account *Account
}

func (s *codexAccountIdentityRepoStub) GetByID(_ context.Context, _ int64) (*Account, error) {
	return s.account, nil
}

func TestCodexRequestBodyIdentityNamespaceIsStablePerOAuthAccount(t *testing.T) {
	body := []byte(`{"model":"gpt-5.6-codex","prompt_cache_key":"client-session","client_metadata":{"x-codex-installation-id":"client-installation","session_id":"client-session","thread_id":"client-thread","x-codex-window-id":"client-window","x-codex-turn-metadata":"{\"installation_id\":\"client-installation\",\"session_id\":\"client-session\",\"thread_id\":\"client-thread\",\"turn_id\":\"client-turn\",\"window_id\":\"client-window\"}"}}`)
	account11 := &Account{ID: 11, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"chatgpt_account_id": "chatgpt-account-11"}}
	account19 := &Account{ID: 19, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"chatgpt_account_id": "chatgpt-account-19"}}

	first, changed, err := applyCodexAccountIdentityClientMetadataRaw(body, account11, 77)
	require.NoError(t, err)
	require.True(t, changed)
	firstAgain, changed, err := applyCodexAccountIdentityClientMetadataRaw(body, account11, 77)
	require.NoError(t, err)
	require.True(t, changed)
	second, changed, err := applyCodexAccountIdentityClientMetadataRaw(body, account19, 77)
	require.NoError(t, err)
	require.True(t, changed)
	require.JSONEq(t, string(first), string(firstAgain))

	paths := []string{
		"prompt_cache_key",
		"client_metadata.x-codex-installation-id",
		"client_metadata.session_id",
		"client_metadata.thread_id",
		"client_metadata.x-codex-window-id",
	}
	for _, path := range paths {
		require.NotEqual(t, gjson.GetBytes(body, path).String(), gjson.GetBytes(first, path).String(), path)
		require.NotEqual(t, gjson.GetBytes(first, path).String(), gjson.GetBytes(second, path).String(), path)
	}
	require.Equal(t, gjson.GetBytes(first, "prompt_cache_key").String(), gjson.GetBytes(first, "client_metadata.session_id").String())

	var embeddedFirst map[string]any
	var embeddedSecond map[string]any
	require.NoError(t, json.Unmarshal([]byte(gjson.GetBytes(first, "client_metadata.x-codex-turn-metadata").String()), &embeddedFirst))
	require.NoError(t, json.Unmarshal([]byte(gjson.GetBytes(second, "client_metadata.x-codex-turn-metadata").String()), &embeddedSecond))
	for _, field := range []string{"installation_id", "session_id", "thread_id", "turn_id", "window_id"} {
		require.NotEqual(t, embeddedFirst[field], embeddedSecond[field], field)
	}
}

func TestCodexAccountIdentityNamespaceUsesStableCredentialSource(t *testing.T) {
	firstRow := &Account{ID: 8, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"chatgpt_account_id": "shared-upstream-account"}}
	secondRow := &Account{ID: 19, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"chatgpt_account_id": "shared-upstream-account"}}
	require.Equal(t, codexAccountIdentityNamespace(firstRow), codexAccountIdentityNamespace(secondRow))

	firstUser := &Account{ID: 20, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"chatgpt_account_id": "team-account", "chatgpt_user_id": "user-1"}}
	sameUser := &Account{ID: 21, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"chatgpt_account_id": "team-account", "chatgpt_user_id": "user-1"}}
	secondUser := &Account{ID: 22, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"chatgpt_account_id": "team-account", "chatgpt_user_id": "user-2"}}
	require.Equal(t, codexAccountIdentityNamespace(firstUser), codexAccountIdentityNamespace(sameUser))
	require.NotEqual(t, codexAccountIdentityNamespace(firstUser), codexAccountIdentityNamespace(secondUser))

	seed := "11111111-1111-4111-8111-111111111111"
	seeded := &Account{ID: 11, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: map[string]any{codexFingerprintSeedExtraKey: seed}}
	require.Equal(t, "seed:"+seed, codexAccountIdentityNamespace(seeded))

	// Local row IDs repeat across independent deployments, so they are not a
	// safe fallback for upstream identity.
	require.Empty(t, codexAccountIdentityNamespace(&Account{ID: 11, Platform: PlatformOpenAI, Type: AccountTypeOAuth}))

	setupTokenA := &Account{ID: 30, Platform: PlatformOpenAI, Type: AccountTypeSetupToken, Credentials: map[string]any{"access_token": "setup-token-a"}}
	setupTokenADuplicate := &Account{ID: 31, Platform: PlatformOpenAI, Type: AccountTypeSetupToken, Credentials: map[string]any{"access_token": "setup-token-a"}}
	setupTokenB := &Account{ID: 32, Platform: PlatformOpenAI, Type: AccountTypeSetupToken, Credentials: map[string]any{"access_token": "setup-token-b"}}
	setupNamespace := codexAccountIdentityNamespace(setupTokenA)
	require.NotEmpty(t, setupNamespace)
	require.NotContains(t, setupNamespace, "setup-token-a")
	require.Equal(t, setupNamespace, codexAccountIdentityNamespace(setupTokenADuplicate))
	require.NotEqual(t, setupNamespace, codexAccountIdentityNamespace(setupTokenB))
}

func TestCodexAccountIdentitySourceResolvesShadowAndOverwritesFailoverContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

	parentID := int64(11)
	parent := &Account{ID: parentID, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{
		"chatgpt_account_id": "team-account",
		"chatgpt_user_id":    "user-1",
	}}
	shadow := &Account{ID: 111, ParentAccountID: &parentID, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	service := &OpenAIGatewayService{accountRepo: &codexAccountIdentityRepoStub{account: parent}}

	resolved, err := service.prepareCodexAccountIdentitySource(context.Background(), c, shadow)
	require.NoError(t, err)
	require.Same(t, parent, resolved)
	require.Same(t, parent, codexAccountIdentitySource(c, shadow))

	req, err := service.buildUpstreamRequest(
		context.Background(), c, shadow,
		[]byte(`{"model":"gpt-5.6-codex","stream":true,"prompt_cache_key":"client-session"}`),
		"token", true, "client-session", true,
	)
	require.NoError(t, err)
	require.Equal(t, isolateOpenAIUpstreamSessionID(0, parent, "client-session"), req.Header.Get("session_id"))

	next := &Account{ID: 19, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{
		"chatgpt_account_id": "other-account",
		"chatgpt_user_id":    "user-2",
	}}
	resolved, err = service.prepareCodexAccountIdentitySource(context.Background(), c, next)
	require.NoError(t, err)
	require.Same(t, next, resolved)
	require.Same(t, next, codexAccountIdentitySource(c, shadow))
}

func TestBuildOpenAIWSHeadersNamespacesCodexIdentityByOAuthAccount(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	c.Set("api_key_id", int64(77))
	c.Request.Header.Set("x-codex-installation-id", "client-installation")
	c.Request.Header.Set("thread-id", "client-thread")
	c.Request.Header.Set("x-codex-window-id", "client-window")
	c.Request.Header.Set("x-client-request-id", "client-request")

	account11 := &Account{ID: 11, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"chatgpt_account_id": "chatgpt-account-11"}}
	account19 := &Account{ID: 19, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"chatgpt_account_id": "chatgpt-account-19"}}
	service := &OpenAIGatewayService{}
	build := func(account *Account) http.Header {
		headers, _, err := service.buildOpenAIWSHeaders(
			context.Background(), c, account, "token",
			OpenAIWSProtocolDecision{Transport: OpenAIUpstreamTransportResponsesWebsocketV2},
			true, "", "", "client-session", "", "",
		)
		require.NoError(t, err)
		return headers
	}

	first := build(account11)
	firstAgain := build(account11)
	second := build(account19)
	for _, header := range []string{"session_id", "x-codex-installation-id", "thread-id", "x-codex-window-id", "x-client-request-id"} {
		require.NotEmpty(t, first.Get(header), header)
		require.Equal(t, first.Get(header), firstAgain.Get(header), header)
		require.NotEqual(t, first.Get(header), second.Get(header), header)
	}

	httpRequest, err := service.buildUpstreamRequest(
		context.Background(), c, account11,
		[]byte(`{"model":"gpt-5.6-codex","stream":true,"prompt_cache_key":"client-session"}`),
		"token", true, "client-session", true,
	)
	require.NoError(t, err)
	require.Equal(t, httpRequest.Header.Get("session_id"), first.Get("session_id"), "HTTP and WS must derive the same identity from the raw client key")
}

func TestBuildUpstreamRequestNamespacesCodexIdentityByOAuthAccount(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := &OpenAIGatewayService{}
	body := []byte(`{"model":"gpt-5.6-codex","stream":true,"prompt_cache_key":"client-session"}`)

	build := func(accountID int64, chatgptAccountID string) http.Header {
		t.Helper()
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
		c.Set("api_key", &APIKey{ID: 77})
		c.Request.Header.Set("User-Agent", "codex_cli_rs/0.144.0")
		c.Request.Header.Set("x-codex-installation-id", "client-installation")
		c.Request.Header.Set("x-codex-window-id", "client-window")
		c.Request.Header.Set("session-id", "client-session")
		c.Request.Header.Set("thread-id", "client-thread")
		c.Request.Header.Set("x-client-request-id", "client-request")
		c.Request.Header.Set("x-codex-turn-metadata", `{"installation_id":"client-installation","session_id":"client-session","thread_id":"client-thread","turn_id":"client-turn","window_id":"client-window"}`)

		account := &Account{
			ID:       accountID,
			Platform: PlatformOpenAI,
			Type:     AccountTypeOAuth,
			Credentials: map[string]any{
				"chatgpt_account_id": chatgptAccountID,
			},
		}
		req, err := svc.buildUpstreamRequest(
			context.Background(), c, account, body, "oauth-token", true, "client-session", true,
		)
		require.NoError(t, err)
		return req.Header
	}

	first := build(11, "chatgpt-account-11")
	firstAgain := build(11, "chatgpt-account-11")
	second := build(19, "chatgpt-account-19")

	identityHeaders := []string{
		"x-codex-installation-id",
		"x-codex-window-id",
		"session-id",
		"session_id",
		"conversation_id",
		"thread-id",
		"x-client-request-id",
		"x-codex-turn-metadata",
	}
	checked := 0
	for _, header := range identityHeaders {
		if first.Get(header) == "" && second.Get(header) == "" {
			continue
		}
		checked++
		require.NotEmpty(t, first.Get(header), header)
		require.Equal(t, first.Get(header), firstAgain.Get(header), "same account must retain stable identity: %s", header)
		require.NotEqual(t, first.Get(header), second.Get(header), "account failover must rotate upstream identity: %s", header)
	}
	require.GreaterOrEqual(t, checked, 5, "test must exercise the real outbound identity surface")
	require.Equal(t, first.Get("session-id"), first.Get("session_id"))
	require.Equal(t, first.Get("session-id"), first.Get("conversation_id"))
	require.Equal(t, first.Get("thread-id"), first.Get("x-client-request-id"))
	require.NotEqual(t, isolateOpenAIUpstreamSessionID(77, &Account{ID: 11, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"chatgpt_account_id": "chatgpt-account-11"}}, "client-session"), first.Get("session_id"))
}

func TestFinalizeCodexOutboundIdentityHeadersReusesScopedSessionID(t *testing.T) {
	headers := http.Header{}
	headers.Set("session-id", "scoped-session")
	headers.Set("session_id", "other-hash")
	headers.Set("conversation_id", "other-hash")
	headers.Set("thread-id", "scoped-thread")
	headers.Set("x-client-request-id", "scoped-request")

	account := &Account{ID: 11, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"chatgpt_account_id": "chatgpt-account-11"}}
	finalizeCodexOutboundIdentityHeaders(headers, account)

	require.Equal(t, "scoped-session", headers.Get("session-id"))
	require.Equal(t, "scoped-session", headers.Get("session_id"))
	require.Equal(t, "scoped-session", headers.Get("conversation_id"))
	require.Equal(t, "scoped-thread", headers.Get("thread-id"))
	require.Equal(t, "scoped-thread", headers.Get("x-client-request-id"))
}

func TestFinalizeCodexOutboundIdentityHeadersSkipsAPIKeyAccounts(t *testing.T) {
	headers := http.Header{}
	headers.Set("session-id", "client-session")
	headers.Set("session_id", "isolated-hash")
	finalizeCodexOutboundIdentityHeaders(headers, &Account{ID: 11, Platform: PlatformOpenAI, Type: AccountTypeAPIKey})
	require.Equal(t, "isolated-hash", headers.Get("session_id"))
}

func TestOpenAIAllowedHeadersIncludeCodexSessionAliases(t *testing.T) {
	for _, header := range []string{"session-id", "thread-id", "x-client-request-id"} {
		require.True(t, openaiAllowedHeaders[header], header)
		require.True(t, openaiPassthroughAllowedHeaders[header], header)
	}
}

func TestBuildUpstreamRequestAlignsOfficialCodexSessionHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-5.6-codex","stream":true,"prompt_cache_key":"client-session"}`)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	c.Set("api_key", &APIKey{ID: 77})
	c.Request.Header.Set("User-Agent", "codex_cli_rs/0.144.0")
	c.Request.Header.Set("session-id", "client-session")
	c.Request.Header.Set("thread-id", "client-thread")
	c.Request.Header.Set("x-client-request-id", "client-request")

	account := &Account{
		ID:          11,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Credentials: map[string]any{"chatgpt_account_id": "chatgpt-account-11"},
	}
	req, err := (&OpenAIGatewayService{}).buildUpstreamRequest(context.Background(), c, account, body, "oauth-token", true, "client-session", true)
	require.NoError(t, err)

	require.Equal(t, req.Header.Get("session-id"), req.Header.Get("session_id"))
	require.Equal(t, req.Header.Get("session-id"), req.Header.Get("conversation_id"))
	require.Equal(t, req.Header.Get("thread-id"), req.Header.Get("x-client-request-id"))
	require.NotEqual(t, "client-session", req.Header.Get("session-id"))
	require.NotEqual(t, isolateOpenAIUpstreamSessionID(77, account, "client-session"), req.Header.Get("session_id"))
	require.Equal(t, 4, countRunes(req.Header.Get("session_id"), '-'))
}

func TestBuildOpenAIWSHeadersAlignsOfficialCodexSessionHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	c.Set("api_key", &APIKey{ID: 77})
	c.Request.Header.Set("session-id", "client-session")
	c.Request.Header.Set("thread-id", "client-thread")
	c.Request.Header.Set("x-client-request-id", "client-request")

	account := &Account{
		ID:          11,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Credentials: map[string]any{"chatgpt_account_id": "chatgpt-account-11"},
	}
	headers, _, err := (&OpenAIGatewayService{}).buildOpenAIWSHeaders(
		context.Background(), c, account, "token",
		OpenAIWSProtocolDecision{Transport: OpenAIUpstreamTransportResponsesWebsocketV2},
		true, "", "", "client-session", "", "",
	)
	require.NoError(t, err)
	require.Equal(t, headers.Get("session-id"), headers.Get("session_id"))
	require.Equal(t, headers.Get("thread-id"), headers.Get("x-client-request-id"))
	require.NotEqual(t, isolateOpenAIUpstreamSessionID(77, account, "client-session"), headers.Get("session_id"))
}

func TestBuildUpstreamRequestOpenAIPassthroughAlignsOfficialCodexSessionHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-5.6-codex","stream":true,"prompt_cache_key":"client-session"}`)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	c.Set("api_key", &APIKey{ID: 77})
	c.Request.Header.Set("session-id", "client-session")
	c.Request.Header.Set("thread-id", "client-thread")
	c.Request.Header.Set("x-client-request-id", "client-request")
	c.Request.Header.Set("originator", "codex_cli_rs")

	account := &Account{
		ID:       11,
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"chatgpt_account_id":       "chatgpt-account-11",
			"openai_oauth_passthrough": true,
		},
	}
	req, err := (&OpenAIGatewayService{}).buildUpstreamRequestOpenAIPassthrough(context.Background(), c, account, body, "oauth-token")
	require.NoError(t, err)
	require.Equal(t, req.Header.Get("session-id"), req.Header.Get("session_id"))
	require.Equal(t, req.Header.Get("session-id"), req.Header.Get("conversation_id"))
	require.Equal(t, req.Header.Get("thread-id"), req.Header.Get("x-client-request-id"))
	require.NotEqual(t, isolateOpenAIUpstreamSessionID(77, account, "client-session"), req.Header.Get("session_id"))
}

func countRunes(s string, r rune) int {
	n := 0
	for _, c := range s {
		if c == r {
			n++
		}
	}
	return n
}

type codexOutboundIdentitySnap struct {
	SessionHyphen     string
	SessionUnderscore string
	ConversationID    string
	ThreadID          string
	ClientRequestID   string
}

func readCodexOutboundIdentitySnap(h http.Header) codexOutboundIdentitySnap {
	return codexOutboundIdentitySnap{
		SessionHyphen:     h.Get("session-id"),
		SessionUnderscore: h.Get("session_id"),
		ConversationID:    h.Get("conversation_id"),
		ThreadID:          h.Get("thread-id"),
		ClientRequestID:   h.Get("x-client-request-id"),
	}
}

func TestCodexOutboundIdentityCaptureMatchesAcrossHTTPPassthroughAndWS(t *testing.T) {
	gin.SetMode(gin.TestMode)
	account := &Account{
		ID:          11,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Credentials: map[string]any{"chatgpt_account_id": "chatgpt-account-11"},
	}
	const apiKeyID int64 = 77
	body := []byte(`{"model":"gpt-5.6-codex","stream":true,"prompt_cache_key":"client-session","client_metadata":{"x-codex-installation-id":"client-installation","session_id":"client-session","thread_id":"client-thread","x-codex-window-id":"client-window"}}`)
	rewritten, changed, err := applyCodexAccountIdentityClientMetadataRaw(body, account, apiKeyID)
	require.NoError(t, err)
	require.True(t, changed)
	promptCacheKey := gjson.GetBytes(rewritten, "prompt_cache_key").String()
	metaSessionID := gjson.GetBytes(rewritten, "client_metadata.session_id").String()
	require.Equal(t, promptCacheKey, metaSessionID)
	require.NotEqual(t, "client-session", promptCacheKey)

	newClient := func() *gin.Context {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
		c.Set("api_key", &APIKey{ID: apiKeyID})
		c.Request.Header.Set("User-Agent", "codex_cli_rs/0.144.0")
		c.Request.Header.Set("originator", "codex_cli_rs")
		c.Request.Header.Set("session-id", "client-session")
		c.Request.Header.Set("thread-id", "client-thread")
		c.Request.Header.Set("x-client-request-id", "client-request")
		c.Request.Header.Set("x-codex-installation-id", "client-installation")
		c.Request.Header.Set("x-codex-window-id", "client-window")
		return c
	}

	svc := &OpenAIGatewayService{}
	httpReq, err := svc.buildUpstreamRequest(context.Background(), newClient(), account, rewritten, "oauth-token", true, "client-session", true)
	require.NoError(t, err)
	passAccount := &Account{
		ID:       account.ID,
		Platform: account.Platform,
		Type:     account.Type,
		Credentials: map[string]any{
			"chatgpt_account_id":       "chatgpt-account-11",
			"openai_oauth_passthrough": true,
		},
	}
	passReq, err := svc.buildUpstreamRequestOpenAIPassthrough(context.Background(), newClient(), passAccount, rewritten, "oauth-token")
	require.NoError(t, err)
	wsHeaders, _, err := svc.buildOpenAIWSHeaders(
		context.Background(), newClient(), account, "oauth-token",
		OpenAIWSProtocolDecision{Transport: OpenAIUpstreamTransportResponsesWebsocketV2},
		true, "", "", "client-session", "", "",
	)
	require.NoError(t, err)

	httpSnap := readCodexOutboundIdentitySnap(httpReq.Header)
	passSnap := readCodexOutboundIdentitySnap(passReq.Header)
	wsSnap := readCodexOutboundIdentitySnap(wsHeaders)
	t.Logf("body.prompt_cache_key=%s client_metadata.session_id=%s", promptCacheKey, metaSessionID)
	t.Logf("http %+v", httpSnap)
	t.Logf("passthrough %+v", passSnap)
	t.Logf("ws %+v", wsSnap)

	for _, snap := range []codexOutboundIdentitySnap{httpSnap, passSnap, wsSnap} {
		require.NotEmpty(t, snap.SessionHyphen)
		require.Equal(t, snap.SessionHyphen, snap.SessionUnderscore)
		require.Equal(t, snap.ThreadID, snap.ClientRequestID)
		require.Equal(t, promptCacheKey, snap.SessionHyphen)
	}
	require.Equal(t, httpSnap.SessionHyphen, passSnap.SessionHyphen)
	require.Equal(t, httpSnap.SessionHyphen, wsSnap.SessionHyphen)
	require.Equal(t, httpSnap.ThreadID, passSnap.ThreadID)
	require.Equal(t, httpSnap.ThreadID, wsSnap.ThreadID)
	require.Equal(t, httpSnap.SessionHyphen, httpSnap.ConversationID)
	require.Equal(t, passSnap.SessionHyphen, passSnap.ConversationID)
	if wsSnap.ConversationID != "" {
		require.Equal(t, wsSnap.SessionHyphen, wsSnap.ConversationID)
	}

	var gotHeader http.Header
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Clone()
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	wire, err := http.NewRequest(http.MethodPost, server.URL, bytes.NewReader(rewritten))
	require.NoError(t, err)
	wire.Header = httpReq.Header.Clone()
	resp, err := server.Client().Do(wire)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, httpSnap.SessionHyphen, gotHeader.Get("session-id"))
	require.Equal(t, httpSnap.SessionHyphen, gotHeader.Get("session_id"))
	require.Equal(t, httpSnap.SessionHyphen, gotHeader.Get("conversation_id"))
	require.Equal(t, httpSnap.ThreadID, gotHeader.Get("thread-id"))
	require.Equal(t, httpSnap.ThreadID, gotHeader.Get("x-client-request-id"))
	require.Equal(t, promptCacheKey, gjson.GetBytes(gotBody, "prompt_cache_key").String())
	require.Equal(t, promptCacheKey, gjson.GetBytes(gotBody, "client_metadata.session_id").String())
}
