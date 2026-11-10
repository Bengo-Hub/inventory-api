package handlers

import "testing"

// Security audit 2026-11-11: a terminal PIN session in the codevertex tenant was minted as platform
// owner, so a cashier there bypassed every permission check and (through the tenant middleware)
// could act for any tenant. A terminal session is never platform owner; only the demo slug marks a
// demo (subscription exemption still comes from the subscriptions entitlement).
func TestTerminalSessionFlags(t *testing.T) {
	for _, slug := range []string{"codevertex", "codevertex-demo", "acme"} {
		isDemo, isPO := terminalSessionFlags(slug)
		if isPO {
			t.Errorf("%s: a terminal PIN session must never be platform owner", slug)
		}
		if isDemo != (slug == "codevertex-demo") {
			t.Errorf("%s: demo flag %v", slug, isDemo)
		}
	}
}
