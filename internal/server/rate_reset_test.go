package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"workbuddy2api/internal/auth"
	"workbuddy2api/internal/upstream"
)

func TestChat429ReportsEarliestReset(t *testing.T) {
	format := func(at time.Time) string {
		return at.In(upstream.SoftRateResetLoc()).Format("2006-01-02 15:04:05")
	}
	early := format(time.Now().Add(5 * time.Minute))
	late := format(time.Now().Add(2 * time.Hour))
	calls := 0
	up := newFakeUpstream(t, func(authz string) (int, string, bool) {
		calls++
		reset := late
		if calls == 1 {
			reset = early
		}
		return 429, `{"code":6004,"msg":"将在 ` + reset + ` UTC+8 重置"}`, false
	})
	p := testPoolWith(
		&auth.Auth{UID: "a", AccessToken: "at-a", ExpiresAt: 9999999999},
		&auth.Auth{UID: "b", AccessToken: "at-b", ExpiresAt: 9999999999},
	)
	h := NewHandler(Config{Pool: p, Upstream: up, MaxRotate: 2})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"glm-5.3","messages":[]}`)))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	if calls != 2 || !strings.Contains(rec.Body.String(), early) || strings.Contains(rec.Body.String(), late) {
		t.Fatalf("want earliest reset %s after two attempts, calls=%d body=%s", early, calls, rec.Body)
	}
}
