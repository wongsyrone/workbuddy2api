package pool

import (
	"testing"
	"time"

	"workbuddy2api/internal/auth"
)

func TestModelCooldownKeepsResetAcrossTransitions(t *testing.T) {
	for _, tc := range []struct {
		name       string
		transition func(*Pool)
	}{
		{"short backoff expires", func(*Pool) {}},
		{"account cooldown", func(p *Pool) { p.Cooldown("u", CoolSoft, time.Minute, "temporary") }},
		{"account rate limit", func(p *Pool) { p.CooldownSoftRate("u", time.Minute, time.Time{}, "temporary") }},
		{"rate limit without reset", func(p *Pool) { p.CooldownSoftForModel("u", time.Minute, time.Time{}, "other-model", "temporary") }},
		{"credits restored", func(p *Pool) { p.ReenableIfCredits("u", 500) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := New("")
			defer p.Close()
			p.Add(&auth.Auth{UID: "u"})
			reset := time.Now().Add(time.Hour)
			p.CooldownSoftForModel("u", time.Minute, reset, "glm-5.3", "6004")
			p.mu.Lock()
			mc := p.byUID["u"].modelCooldowns["glm-5.3"]
			mc.Until = time.Now().Add(-time.Second)
			p.byUID["u"].modelCooldowns["glm-5.3"] = mc
			p.mu.Unlock()
			tc.transition(p)
			st, _ := p.Status("u")
			if len(st.RateLimitedModels) != 1 || !st.RateLimitedModels[0].ResetAt.Equal(reset) {
				t.Fatalf("authoritative reset must survive %s: %+v", tc.name, st.RateLimitedModels)
			}
		})
	}
}
