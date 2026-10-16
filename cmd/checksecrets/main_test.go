package main

import (
	"strings"
	"testing"
)

func TestCredentialDetection(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		secret     bool
	}{{"placeholder", "ANTHROPIC_API_KEY=", false}, {"token", "sk-" + "ant-" + strings.Repeat("x", 25), true}, {"private key", "-----BEGIN " + "PRIVATE KEY-----", true}, {"public docs", "No model credentials configured", false}} {
		t.Run(tc.name, func(t *testing.T) {
			if containsSecret([]byte(tc.text)) != tc.secret {
				t.Fatal(tc.name)
			}
		})
	}
}
