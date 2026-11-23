package handlers

import "testing"

func TestNormalizeSupplierPhone(t *testing.T) {
	cases := []struct {
		in, want string
		ok       bool
	}{
		{"", "", true},
		{"+254", "", true},
		{"+254786", "", true},
		{"0712345678", "+254712345678", true},
		{"+254 254 712 345 678", "+254712345678", true},
		{"+2542547171061", "", false},
		{"+2547072920000", "", false},
	}
	for _, c := range cases {
		got, ok := normalizeSupplierPhone(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("normalizeSupplierPhone(%q) = %q, %v; want %q, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}
