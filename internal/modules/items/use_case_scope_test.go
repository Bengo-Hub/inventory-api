package items

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/bengobox/inventory-service/internal/ent/item"
)

// TestAllowedItemUseCases pins the outlet to item use_case map that mirrors inventory-ui's
// itemUseCases, plus the rule that PROFESSIONAL_SERVICE is allowed at every restricted outlet.
func TestAllowedItemUseCases(t *testing.T) {
	cases := []struct {
		outlet  string
		allowed []item.UseCase
		blocked []item.UseCase
	}{
		{"retail", []item.UseCase{item.UseCaseRETAIL, item.UseCasePROFESSIONAL_SERVICE},
			[]item.UseCase{item.UseCaseFOOD_BEVERAGE, item.UseCaseSALON_SERVICE, item.UseCasePHARMACY}},
		{"manufacturing", []item.UseCase{item.UseCaseRETAIL}, []item.UseCase{item.UseCaseFOOD_BEVERAGE}},
		{"Hospitality", []item.UseCase{item.UseCaseFOOD_BEVERAGE, item.UseCaseHOSPITALITY_ROOM, item.UseCaseCONFERENCE,
			item.UseCaseAMENITY, item.UseCaseRETAIL, item.UseCasePROFESSIONAL_SERVICE},
			[]item.UseCase{item.UseCaseSALON_SERVICE, item.UseCasePHARMACY}},
		{"quick_service", []item.UseCase{item.UseCaseFOOD_BEVERAGE, item.UseCaseRETAIL}, []item.UseCase{item.UseCaseHOSPITALITY_ROOM}},
		{"pharmacy", []item.UseCase{item.UseCasePHARMACY, item.UseCasePROFESSIONAL_SERVICE}, []item.UseCase{item.UseCaseRETAIL}},
		{"services", []item.UseCase{item.UseCaseSALON_SERVICE, item.UseCasePROFESSIONAL_SERVICE, item.UseCaseAMENITY},
			[]item.UseCase{item.UseCaseRETAIL, item.UseCaseFOOD_BEVERAGE}},
		{"warehouse", []item.UseCase{item.UseCaseRETAIL, item.UseCaseFOOD_BEVERAGE, item.UseCasePHARMACY},
			[]item.UseCase{item.UseCaseSPA_SERVICE}},
	}
	for _, tc := range cases {
		got, restricted := AllowedItemUseCases(tc.outlet)
		require.True(t, restricted, tc.outlet)
		for _, uc := range tc.allowed {
			require.Contains(t, got, uc, "%s should allow %s", tc.outlet, uc)
		}
		for _, uc := range tc.blocked {
			require.NotContains(t, got, uc, "%s should not allow %s", tc.outlet, uc)
		}
	}

	// services already lists PROFESSIONAL_SERVICE: it is not added twice.
	got, _ := AllowedItemUseCases("services")
	n := 0
	for _, uc := range got {
		if uc == item.UseCasePROFESSIONAL_SERVICE {
			n++
		}
	}
	require.Equal(t, 1, n)

	for _, open := range []string{"", "hospital", "general", "wholesale", "unknown"} {
		_, restricted := AllowedItemUseCases(open)
		require.False(t, restricted, open)
	}
}
