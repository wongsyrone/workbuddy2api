package main

import (
	"strings"
	"testing"
	"time"
)

func TestLargeRequestDefaultThreshold(t *testing.T) {
	c, err := Load(writeCfg(t, `{}`))
	if err != nil {
		t.Fatal(err)
	}
	if c.Pool.LargeRequestBytes != 2<<20 {
		t.Fatalf("large_request_bytes=%d, want 2097152", c.Pool.LargeRequestBytes)
	}
}

func TestLargeRequestBudgetConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		budget     int
		window     time.Duration
	}{
		{"default", `{}`, 6, 120 * time.Second},
		{"explicit", `{"pool":{"large_request_budget":2,"large_request_budget_window":"30s"}}`, 2, 30 * time.Second},
		{"disabled", `{"pool":{"large_request_budget":-1}}`, -1, 120 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, err := Load(writeCfg(t, tc.body))
			if err != nil {
				t.Fatal(err)
			}
			if c.Pool.LargeRequestBudget != tc.budget || c.LargeRequestBudgetWindowDur != tc.window {
				t.Fatalf("budget=%d window=%v, want %d/%v", c.Pool.LargeRequestBudget, c.LargeRequestBudgetWindowDur, tc.budget, tc.window)
			}
		})
	}
	for _, window := range []string{"bad", "0s", "-1s"} {
		if _, err := Load(writeCfg(t, `{"pool":{"large_request_budget_window":"`+window+`"}}`)); err == nil || !strings.Contains(err.Error(), "large_request_budget_window") {
			t.Fatalf("window %q: want validation error, got %v", window, err)
		}
	}
	c, err := Load("../../config.example.json")
	if err != nil {
		t.Fatal(err)
	}
	if c.Pool.LargeRequestBytes != 2<<20 || c.Pool.LargeRequestBudget != 6 || c.LargeRequestBudgetWindowDur != 120*time.Second {
		t.Fatal("example must use the default large request budget")
	}
}
