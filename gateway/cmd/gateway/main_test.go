package main

import "testing"

func TestHealthURLTargetsLoopback(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		":8080":           "http://127.0.0.1:8080/healthz",
		"0.0.0.0:8080":    "http://127.0.0.1:8080/healthz",
		"[::]:8080":       "http://127.0.0.1:8080/healthz",
		"127.0.0.1:18080": "http://127.0.0.1:18080/healthz",
	}

	for addr, want := range tests {
		t.Run(addr, func(t *testing.T) {
			t.Parallel()

			if got := healthURL(addr); got != want {
				t.Fatalf("healthURL(%q) = %q, want %q", addr, got, want)
			}
		})
	}
}
