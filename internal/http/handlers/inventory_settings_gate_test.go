package handlers

import (
	"testing"

	"github.com/bengobox/inventory-service/internal/ent"
)

func boolPtr(v bool) *bool { return &v }

// The settings page resends every toggle on save. Only turning a locked toggle on is refused, so
// a downgraded tenant whose stored toggle is still on can keep saving its other settings.
func TestLockedSettingEnable(t *testing.T) {
	none := func(string) bool { return false }
	all := func(string) bool { return true }

	cases := []struct {
		name    string
		input   updateInventorySettingsInput
		stored  ent.TenantInventoryConfig
		enabled func(string) bool
		want    string
	}{
		{
			name:    "turning a locked toggle on is refused",
			input:   updateInventorySettingsInput{EnableLowStockNotifications: boolPtr(true)},
			enabled: none,
			want:    "stock_alerts",
		},
		{
			name:    "resending a stored true toggle after a downgrade is allowed",
			input:   updateInventorySettingsInput{EnableLowStockNotifications: boolPtr(true), EnableLotTracking: boolPtr(true)},
			stored:  ent.TenantInventoryConfig{EnableLowStockNotifications: true, EnableLotTracking: true},
			enabled: none,
		},
		{
			name:    "resending false toggles is allowed",
			input:   updateInventorySettingsInput{EnableExpiryTracking: boolPtr(false), PerOutletPricingEnabled: boolPtr(false)},
			enabled: none,
		},
		{
			name:    "turning a stored true toggle off is allowed",
			input:   updateInventorySettingsInput{BatchPeriodPricingEnabled: boolPtr(false)},
			stored:  ent.TenantInventoryConfig{BatchPeriodPricingEnabled: true},
			enabled: none,
		},
		{
			name:    "a plan with the feature may turn it on",
			input:   updateInventorySettingsInput{EnableExpiryNotifications: boolPtr(true)},
			enabled: all,
		},
		{
			name: "the newly enabled toggle is reported, not the one already on",
			input: updateInventorySettingsInput{
				EnableLowStockNotifications: boolPtr(true),
				PerOutletPricingEnabled:     boolPtr(true),
			},
			stored:  ent.TenantInventoryConfig{EnableLowStockNotifications: true},
			enabled: none,
			want:    "multi_branch_pricing",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stored := tc.stored
			if got := lockedSettingEnable(tc.input, &stored, tc.enabled); got != tc.want {
				t.Fatalf("lockedSettingEnable = %q, want %q", got, tc.want)
			}
		})
	}
}
