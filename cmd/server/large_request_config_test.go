package main

import (
	"testing"
	"time"
)

func TestLargeRequestRotationConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name, cooldown string
		want           time.Duration
	}{
		{"default disabled", "", 0},
		{"explicit disabled", "0s", 0},
		{"explicit enabled", "90s", 90 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := Default()
			c.Pool.LargeRequestCooldown = tc.cooldown
			if err := c.normalize(); err != nil {
				t.Fatal(err)
			}
			if c.LargeRequestCooldownDur != tc.want {
				t.Fatalf("cooldown = %v, want %v", c.LargeRequestCooldownDur, tc.want)
			}
		})
	}
}
