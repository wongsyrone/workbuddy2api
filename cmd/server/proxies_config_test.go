package main

import "testing"

func TestProxyConfiguration(t *testing.T) {
	for _, address := range []string{"ftp://example.com:21", "socks5://", "http://%"} {
		t.Run(address, func(t *testing.T) {
			c := Default()
			c.Proxies = map[string]string{"route": address}
			if err := c.normalize(); err == nil {
				t.Fatalf("invalid proxy address accepted: %q", address)
			}
		})
	}
	c := Default()
	t.Setenv("WB2A_PROXIES", `{" route ":" socks5h://localhost:1080 "}`)
	applyEnv(c)
	if err := c.normalize(); err != nil {
		t.Fatal(err)
	}
	if c.Proxies["route"] != "socks5h://localhost:1080" {
		t.Fatalf("proxy override was not normalized: %v", c.Proxies)
	}
}
