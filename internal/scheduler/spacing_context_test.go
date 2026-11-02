package scheduler

import (
	"context"
	"errors"
	"testing"

	"workbuddy2api/internal/auth"
	"workbuddy2api/internal/pool"
	"workbuddy2api/internal/upstream"
)

func TestCancelledCheckinDoesNotContactUpstream(t *testing.T) {
	p := pool.New("")
	p.Add(&auth.Auth{UID: "u1", AccessToken: "at", RefreshToken: "rt", ExpiresAt: 9999999999})
	s := New(Config{Pool: p, Upstream: &upstream.Client{}})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out, err := s.checkinAll(ctx)
	if !errors.Is(err, context.Canceled) || len(out) != 0 {
		t.Fatalf("cancelled checkin = %v, %v", out, err)
	}
}
