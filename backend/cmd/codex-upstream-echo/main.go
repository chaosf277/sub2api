// codex-upstream-echo prints Codex identity headers and body cache keys.
// Point gateway.debug_codex_upstream_url at this listener to inspect outbound
// OAuth requests without sending them to chatgpt.com.
//
//	go run ./cmd/codex-upstream-echo -listen 127.0.0.1:9977
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:9977", "listen address")
	flag.Parse()
	mux := http.NewServeMux()
	mux.HandleFunc("/", echo)
	log.Printf("codex-upstream-echo listening on http://%s", *listen)
	if err := http.ListenAndServe(*listen, mux); err != nil {
		log.Fatal(err)
	}
}

func echo(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	_ = r.Body.Close()
	sessionHyphen := strings.TrimSpace(r.Header.Get("session-id"))
	sessionUnderscore := strings.TrimSpace(r.Header.Get("session_id"))
	conversationID := strings.TrimSpace(r.Header.Get("conversation_id"))
	threadID := strings.TrimSpace(r.Header.Get("thread-id"))
	clientRequestID := strings.TrimSpace(r.Header.Get("x-client-request-id"))
	promptCacheKey, metaSession := extractCacheIdentity(body)
	sameSession := sessionHyphen != "" && sessionHyphen == sessionUnderscore && (conversationID == "" || conversationID == sessionHyphen) && (promptCacheKey == "" || promptCacheKey == sessionHyphen) && (metaSession == "" || metaSession == sessionHyphen)
	sameThread := threadID == "" || threadID == clientRequestID
	fmt.Fprintf(os.Stderr, "method=%s path=%s host=%s\n", r.Method, r.URL.RequestURI(), r.Host)
	fmt.Fprintf(os.Stderr, "session-id=%s session_id=%s conversation_id=%s\n", sessionHyphen, sessionUnderscore, conversationID)
	fmt.Fprintf(os.Stderr, "thread-id=%s x-client-request-id=%s\n", threadID, clientRequestID)
	fmt.Fprintf(os.Stderr, "prompt_cache_key=%s client_metadata.session_id=%s\n", promptCacheKey, metaSession)
	if sameSession && sameThread {
		fmt.Fprintln(os.Stderr, "verdict=ALIGNED")
	} else {
		fmt.Fprintln(os.Stderr, "verdict=SPLIT")
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"id":"echo","object":"response","status":"completed"}`))
}

func extractCacheIdentity(body []byte) (promptCacheKey, metaSession string) {
	var payload struct {
		PromptCacheKey string `json:"prompt_cache_key"`
		ClientMetadata struct {
			SessionID string `json:"session_id"`
		} `json:"client_metadata"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", ""
	}
	return strings.TrimSpace(payload.PromptCacheKey), strings.TrimSpace(payload.ClientMetadata.SessionID)
}
