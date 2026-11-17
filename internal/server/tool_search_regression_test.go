package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"workbuddy2api/internal/auth"
	"workbuddy2api/internal/pool"
	"workbuddy2api/internal/upstream"
)

func TestToolSearchRespectsKeyGroups(t *testing.T) {
	for _, cooling := range []bool{false, true} {
		t.Run(map[bool]string{false: "healthy", true: "cooldown fallback"}[cooling], func(t *testing.T) {
			rec := &searchRecorder{}
			h := newSearchHandler(newSearchUpstream(t, rec, searchOK),
				&auth.Auth{UID: "other", AccessToken: "other-token", Groups: []string{"other"}})
			h.cfg.AuthKeys = []AuthKey{{Key: "team-key", Groups: []string{"team"}}}
			if cooling {
				h.cfg.Pool.Cooldown("other", pool.CoolSoft, time.Minute, "test")
			}
			req := searchReq(`{"query":"go"}`)
			req.Header.Set("Authorization", "Bearer team-key")
			res := httptest.NewRecorder()
			h.ServeHTTP(res, req)
			if res.Code != http.StatusServiceUnavailable || rec.callCount() != 0 {
				t.Fatalf("search crossed key groups: status=%d calls=%d", res.Code, rec.callCount())
			}
		})
	}
}

func TestToolSearchAppliesErrorPolicyOnce(t *testing.T) {
	for _, tc := range []struct {
		status int
		errors int64
	}{
		{http.StatusInternalServerError, 1},
		{http.StatusTooManyRequests, 0},
		{http.StatusNotFound, 0},
	} {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			up := newFakeUpstream(t, func(string) (int, string, bool) { return tc.status, "upstream error", false })
			up.AgentToolBaseOverride = "https://fake.example"
			h := newSearchHandler(up, &auth.Auth{UID: "u", AccessToken: "at", ExpiresAt: 9999999999})
			h.cfg.MaxRotate = 1
			h.ServeHTTP(httptest.NewRecorder(), searchReq(`{"query":"go"}`))
			st, _ := h.cfg.Pool.Status("u")
			if st.ErrTotal != tc.errors || st.InFlight != 0 {
				t.Fatalf("policy must run once and release the lease: errors=%d in_flight=%d", st.ErrTotal, st.InFlight)
			}
		})
	}
}

func TestToolSearchCancellationDoesNotPenalize(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	up := newFakeUpstream(t, func(string) (int, string, bool) {
		cancel()
		return http.StatusInternalServerError, "cancelled request", false
	})
	up.AgentToolBaseOverride = "https://fake.example"
	h := newSearchHandler(up, &auth.Auth{UID: "u", AccessToken: "at", ExpiresAt: 9999999999})
	h.ServeHTTP(httptest.NewRecorder(), searchReq(`{"query":"go"}`).WithContext(ctx))
	st, _ := h.cfg.Pool.Status("u")
	if st.ErrTotal != 0 || st.BreakerFails != 0 || st.InFlight != 0 {
		t.Fatalf("client cancellation must only release the lease: %+v", st)
	}
}

func TestToolSearchValidatesBodyBeforeCallingUpstream(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"invalid type", `{"query":"go","type":"image"}`, http.StatusBadRequest},
		{"trailing JSON", `{"query":"go"} {}`, http.StatusBadRequest},
		{"trailing garbage", `{"query":"go"} garbage`, http.StatusBadRequest},
		{"oversized", `{"query":"` + strings.Repeat("x", 1<<20) + `"}`, http.StatusRequestEntityTooLarge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := &searchRecorder{}
			h := newSearchHandler(newSearchUpstream(t, rec, searchOK), &auth.Auth{UID: "u"})
			res := httptest.NewRecorder()
			h.ServeHTTP(res, searchReq(tc.body))
			if res.Code != tc.status || rec.callCount() != 0 {
				t.Fatalf("want status=%d without an upstream call, got status=%d calls=%d", tc.status, res.Code, rec.callCount())
			}
		})
	}
}

func TestToolSearchRefreshesExpiringToken(t *testing.T) {
	refreshes := 0
	up := &upstream.Client{
		ChatBaseCN:            "https://fake.example",
		AgentToolBaseOverride: "https://fake.example",
		HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			body := searchOK
			if r.URL.Path == "/v2/plugin/auth/token/refresh" {
				refreshes++
				body = `{"code":0,"data":{"accessToken":"new-at","refreshToken":"new-rt","expiresIn":3600}}`
			} else if r.Header.Get("Authorization") != "Bearer new-at" {
				t.Error("search used an expired token")
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(body)),
			}, nil
		})},
	}
	path := filepath.Join(t.TempDir(), "account.json")
	h := newSearchHandler(up, &auth.Auth{
		UID: "u", AccessToken: "old-at", RefreshToken: "old-rt", ExpiresAt: 1, FilePath: path,
	})
	res := httptest.NewRecorder()
	h.ServeHTTP(res, searchReq(`{"query":"go"}`))
	if res.Code != http.StatusOK || refreshes != 1 {
		t.Fatalf("search must refresh once before sending: status=%d refreshes=%d", res.Code, refreshes)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var saved map[string]json.RawMessage
	if err := json.Unmarshal(raw, &saved); err != nil || !strings.Contains(string(saved["auth"]), "new-at") {
		t.Fatalf("refreshed credential was not persisted: err=%v", err)
	}
}

func TestToolSearchPassesOptionalParameters(t *testing.T) {
	rec := &searchRecorder{}
	h := newSearchHandler(newSearchUpstream(t, rec, searchOK),
		&auth.Auth{UID: "u", AccessToken: "at", ExpiresAt: 9999999999, Groups: []string{"team"}})
	h.cfg.AuthKeys = []AuthKey{{Key: "team-key", Groups: []string{"team"}}}
	req := searchReq(`{"query":" go ","type":"text2text","max_results":99,"allowed_domains":["go.dev"],"blocked_domains":[" reddit.com ",""],"freshness":"y1"}`)
	req.Header.Set("Authorization", "Bearer team-key")
	res := httptest.NewRecorder()
	h.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("matching group must be allowed: status=%d", res.Code)
	}
	var body upstream.AgentToolSearchRequest
	raw, err := json.Marshal(rec.requestBodies()[0])
	if err != nil || json.Unmarshal(raw, &body) != nil {
		t.Fatalf("cannot decode outgoing parameters: %v", err)
	}
	if body.Query != "go -site:reddit.com" || body.MaxResults != 20 || body.Freshness != "y1" || len(body.AllowedDomains) != 1 || body.AllowedDomains[0] != "go.dev" {
		t.Fatalf("outgoing parameters: %+v", body)
	}
}

func TestToolSearchGlobalQueueDoesNotPenalizeOrRotate(t *testing.T) {
	calls := 0
	up := newFakeUpstream(t, func(string) (int, string, bool) {
		calls++
		return http.StatusTooManyRequests, `{"code":6020,"msg":"queue.waiting.title"}`, false
	})
	up.AgentToolBaseOverride = "https://fake.example"
	h := newSearchHandler(up,
		&auth.Auth{UID: "a", AccessToken: "at-a", ExpiresAt: 9999999999},
		&auth.Auth{UID: "b", AccessToken: "at-b", ExpiresAt: 9999999999})
	res := httptest.NewRecorder()
	h.ServeHTTP(res, searchReq(`{"query":"go"}`))
	if res.Code != http.StatusTooManyRequests || calls != 1 {
		t.Fatalf("global queue must return 429 without account rotation: status=%d calls=%d", res.Code, calls)
	}
	for _, st := range h.cfg.Pool.List() {
		if st.Cooling || st.Disabled || st.ErrTotal != 0 || st.InFlight != 0 {
			t.Fatalf("global queue penalized an account or leaked its lease: %+v", st)
		}
	}
}

func TestToolSearchQuotaDoesNotMaskOtherFailures(t *testing.T) {
	calls := 0
	up := newFakeUpstream(t, func(string) (int, string, bool) {
		calls++
		if calls == 1 {
			return http.StatusOK, `{"code":15001,"msg":"quota_web_search"}`, false
		}
		return http.StatusInternalServerError, "server failure", false
	})
	up.AgentToolBaseOverride = "https://fake.example"
	h := newSearchHandler(up,
		&auth.Auth{UID: "a", AccessToken: "at-a", ExpiresAt: 9999999999},
		&auth.Auth{UID: "b", AccessToken: "at-b", ExpiresAt: 9999999999})
	h.cfg.MaxRotate = 2
	res := httptest.NewRecorder()
	h.ServeHTTP(res, searchReq(`{"query":"go"}`))
	if res.Code != http.StatusInternalServerError || !strings.Contains(res.Body.String(), "server failure") {
		t.Fatalf("one quota failure must not hide another upstream error: status=%d body=%s", res.Code, res.Body)
	}
}
