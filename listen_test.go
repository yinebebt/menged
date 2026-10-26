package main

import "testing"

func TestLoopbackListen(t *testing.T) {
	t.Parallel()

	cases := []struct {
		addr string
		want bool
	}{
		{"127.0.0.1:8080", true},
		{"localhost:8080", true},
		{"[::1]:8080", true},
		{":8080", false},
		{"0.0.0.0:8080", false},
		{"192.168.1.9:8080", false},
		{"not-an-addr", false},
	}
	for _, tc := range cases {
		t.Run(tc.addr, func(t *testing.T) {
			t.Parallel()
			if got := loopbackListen(tc.addr); got != tc.want {
				t.Fatalf("loopbackListen(%q) = %v, want %v", tc.addr, got, tc.want)
			}
		})
	}
}
