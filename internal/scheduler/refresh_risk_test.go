package scheduler

import (
	"testing"
	"time"

	"workbuddy2api/internal/auth"
	"workbuddy2api/internal/pool"
	"workbuddy2api/internal/upstream"
)

func TestRefreshRiskPausesScheduledWork(t *testing.T) {
	p := pool.New("")
	p.Add(&auth.Auth{UID: "u1", AccessToken: "at", RefreshToken: "rt", ExpiresAt: 9999999999})
	p.PauseRefreshRateLimited("u1", time.Hour)
	s := New(Config{Pool: p, Upstream: &upstream.Client{}})
	original := newScriptCmd
	t.Cleanup(func() { newScriptCmd = original })
	newScriptCmd = func(string, ...string) scriptRunner {
		t.Fatal("paused account must not start a task script")
		return nil
	}
	out, err := s.CheckinAll()
	if err != nil || len(out) != 1 || out[0].Status != CheckinSkipped {
		t.Fatalf("paused checkin = %v, %v", out, err)
	}
	s.RunActivityNow()
	s.RunTravelNow()
	s.RunKeepaliveNow()
	s.RunSchoolNow()
	s.RunCatNow()
}
