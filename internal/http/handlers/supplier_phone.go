package handlers

import (
	"strings"

	"github.com/Bengo-Hub/httpware/contact"
)

// normalizeSupplierPhone stores a supplier phone in E.164 (a repeated country code is repaired).
// A field left at its "+254" prefix, or with only a few digits typed after it, is no number: it is
// stored empty instead of as a half-typed value (11 suppliers held one on 2026-11-24). ok is false
// for a value that is neither empty nor a valid number.
func normalizeSupplierPhone(raw string) (phone string, ok bool) {
	digits := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, raw)
	if len(digits) <= 6 {
		return "", true
	}
	p, err := contact.NormalizePhone(raw, "")
	if err != nil {
		return "", false
	}
	return p, true
}
