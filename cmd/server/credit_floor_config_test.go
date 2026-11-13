package main

import "testing"

func TestCreditFloorConfiguration(t *testing.T) {
	for _, tc := range []struct {
		body string
		want int64
	}{
		{`{}`, 0},
		{`{"pool":{"credit_floor":200}}`, 200},
		{`{"pool":{"credit_floor":-1}}`, 0},
	} {
		c, err := Load(writeCfg(t, tc.body))
		if err != nil {
			t.Fatal(err)
		}
		if c.Pool.CreditFloor != tc.want {
			t.Fatalf("credit_floor=%d, want %d", c.Pool.CreditFloor, tc.want)
		}
	}
}
