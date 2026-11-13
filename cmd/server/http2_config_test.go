package main

import "testing"

func TestHTTP2Configuration(t *testing.T) {
	t.Setenv("WB2A_HTTP2", "")
	path := writeCfg(t, `{}`)
	c, err := Load(path)
	if err != nil || c.Upstream.HTTP2 {
		t.Fatalf("HTTP/2 must default off: config=%+v err=%v", c, err)
	}
	path = writeCfg(t, `{"upstream":{"http2":true}}`)
	c, err = Load(path)
	if err != nil || !c.Upstream.HTTP2 {
		t.Fatalf("explicit HTTP/2 must enable it: config=%+v err=%v", c, err)
	}
	t.Setenv("WB2A_HTTP2", "false")
	c, err = Load(path)
	if err != nil || c.Upstream.HTTP2 {
		t.Fatalf("environment must override the file: config=%+v err=%v", c, err)
	}
	t.Setenv("WB2A_HTTP2", "invalid")
	c, err = Load(path)
	if err != nil || !c.Upstream.HTTP2 {
		t.Fatalf("invalid environment must preserve the file: config=%+v err=%v", c, err)
	}
}
