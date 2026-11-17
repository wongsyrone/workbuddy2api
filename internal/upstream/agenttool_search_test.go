package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"workbuddy2api/internal/auth"
)

func TestAgentToolSearchRealmAndHeaders(t *testing.T) {
	for _, tc := range []struct {
		realm, domain, host, origin string
	}{
		{"cn", "workbuddy.cn", "www.workbuddy.cn", originRefererCN},
		{"global", "www.workbuddy.ai", "www.workbuddy.ai", originRefererGlobal},
	} {
		t.Run(tc.realm, func(t *testing.T) {
			var previousID string
			c := &Client{HTTP: &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.Host != tc.host || r.URL.Path != AgentToolSearchPath || r.Method != http.MethodPost {
					t.Errorf("unexpected search target: %s %s", r.Method, r.URL)
				}
				for key, want := range map[string]string{
					"Authorization": "Bearer at", "X-User-Id": "u", "X-Device-Token": "device",
					"Content-Type": "application/json", "Origin": tc.origin, "X-CodeBuddy-Request": "1",
				} {
					if got := r.Header.Get(key); got != want {
						t.Errorf("%s=%q, want %q", key, got, want)
					}
				}
				if tc.realm == "global" && (r.Header.Get("X-Enterprise-Id") != "" || r.Header.Get("X-Domain") != "") {
					t.Error("global request inherited CN-only headers")
				}
				if tc.realm == "cn" && r.Header.Get("X-Enterprise-Id") != "enterprise" {
					t.Error("CN enterprise header missing")
				}
				id := r.Header.Get("X-Conversation-ID")
				if id == "" || id == previousID || r.Header.Get("X-Conversation-Request-ID") == "" {
					t.Error("search must use fresh conversation headers for each request")
				}
				previousID = id
				deadline, ok := r.Context().Deadline()
				if !ok || time.Until(deadline) > agenttoolSearchTimeout || time.Until(deadline) <= 0 {
					t.Error("search request must have a bounded deadline")
				}
				var body AgentToolSearchRequest
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				if body.Type != "text2text" || body.MaxResults != 5 || len(body.AllowedDomains) != 1 || body.AllowedDomains[0] != "go.dev" || body.Freshness != "y1" {
					t.Errorf("search parameters changed: %+v", body)
				}
				return jsonResp(200, `{"code":0,"results":[],"total_results":0}`), nil
			})}}
			a := &auth.Auth{UID: "u", AccessToken: "at", Domain: tc.domain, EnterpriseID: "enterprise", DeviceToken: "device"}
			for i := 0; i < 2; i++ {
				if _, err := c.AgentToolSearch(context.Background(), a, AgentToolSearchRequest{Query: "go", AllowedDomains: []string{"go.dev"}, Freshness: "y1"}); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestAgentToolSearchErrorClassification(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		kind   ErrKind
	}{
		{"rate limit", 429, `{"code":6004,"msg":"rate limited"}`, ErrSoftRate},
		{"auth rejected", 401, `{}`, ErrAuthRejected},
		{"server failure", 500, "unavailable", ErrServer},
		{"invalid JSON", 200, "<html>bad gateway</html>", ErrServer},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &Client{AgentToolBaseOverride: "https://fake.example", HTTP: &http.Client{
				Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
					resp := jsonResp(tc.status, tc.body)
					resp.Header.Set("Retry-After", "90")
					return resp, nil
				}),
			}}
			_, err := c.AgentToolSearch(context.Background(), &auth.Auth{UID: "u"}, AgentToolSearchRequest{Query: "go"})
			var ue *Error
			if !errors.As(err, &ue) || ue.Kind != tc.kind {
				t.Fatalf("kind=%v, want %v", err, tc.kind)
			}
			if tc.status == 429 && ue.RetryAfter != 90*time.Second {
				t.Fatalf("Retry-After lost: %v", ue.RetryAfter)
			}
		})
	}
}

func TestAgentToolSearchDoesNotFallBackFromUnknownProxy(t *testing.T) {
	c := &Client{HTTP: &http.Client{Transport: rtFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("an unknown account proxy must not fall back to a direct request")
		return nil, nil
	})}}
	_, err := c.AgentToolSearch(context.Background(), &auth.Auth{UID: "u", ProxyName: "missing-search-proxy"}, AgentToolSearchRequest{Query: "go"})
	if err == nil {
		t.Fatal("missing account proxy must return an error")
	}
}

func TestAgentToolSearchPreservesFullError(t *testing.T) {
	body := `{"code":11133,"msg":"` + strings.Repeat("message ", 100) + `"}`
	c := &Client{HTTP: &http.Client{Transport: rtFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 400, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}}
	_, err := c.AgentToolSearch(context.Background(), &auth.Auth{UID: "u"}, AgentToolSearchRequest{Query: "go"})
	var ue *Error
	if !errors.As(err, &ue) || ue.Msg != body {
		t.Fatal("search error was truncated")
	}
}
