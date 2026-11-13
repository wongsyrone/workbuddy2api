package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"workbuddy2api/internal/auth"
)

func TestChatUpstreamQueueDoesNotPenalizeAccount(t *testing.T) {
	const body = `{"code":6020,"msg":"queue.waiting.title","data":{"queue_position":5580,"retry_after":40}}`
	up := newFakeUpstream(t, func(string) (int, string, bool) {
		return http.StatusTooManyRequests, body, false
	})
	p := testPoolWith(&auth.Auth{UID: "queued", AccessToken: "at-queued", ExpiresAt: 9999999999})
	h := NewHandler(Config{Pool: p, Upstream: up, MaxRotate: 1})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"glm-5.3","messages":[]}`)))
	if rec.Code != http.StatusTooManyRequests || !strings.Contains(rec.Body.String(), "queue.waiting.title") {
		t.Fatalf("queue response must remain 429 with upstream details: %d %s", rec.Code, rec.Body)
	}
	st, ok := p.Status("queued")
	if !ok || st.Cooling || st.Disabled || st.SoftStreak != 0 || st.ConsecutiveFails != 0 {
		t.Fatalf("global queue must not penalize the account: %+v", st)
	}
}
