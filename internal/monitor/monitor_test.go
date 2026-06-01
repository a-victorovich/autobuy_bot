package monitor

import (
	"testing"

	"github.com/yourorg/nft-scanner/internal/config"
	getgemsapi "github.com/yourorg/nft-scanner/internal/getgems/openapi"
)

func TestCalculateThreshold(t *testing.T) {
	tests := []struct {
		name        string
		floorPrice  int64
		discountPct float64
		want        int64
	}{
		{
			name:        "zero discount keeps floor price",
			floorPrice:  100,
			discountPct: 0,
			want:        100,
		},
		{
			name:        "ten percent discount lowers threshold",
			floorPrice:  100,
			discountPct: 10,
			want:        90,
		},
		{
			name:        "full discount becomes zero",
			floorPrice:  100,
			discountPct: 100,
			want:        0,
		},
		{
			name:        "works with nano values",
			floorPrice:  2_500_000_000,
			discountPct: 20,
			want:        2_000_000_000,
		},
		{
			name:        "works with nano values and minus discountPct",
			floorPrice:  2_500_000_000,
			discountPct: -20,
			want:        3_000_000_000,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := calculateThreshold(tt.floorPrice, tt.discountPct)
			if got != tt.want {
				t.Fatalf("calculateThreshold(%v, %v) = %v, want %v", tt.floorPrice, tt.discountPct, got, tt.want)
			}
		})
	}
}

func TestMatchAttributeThreshold(t *testing.T) {
	attributes := []getgemsapi.NftItemAttribute{
		{TraitType: "Background", Value: "Blue"},
		{TraitType: "Hat", Value: "Cap"},
	}
	thresholds := []config.AttributePriceThreshold{
		{TraitType: "Background", Value: "Blue", Price: 1.5},
		{TraitType: "Hat", Value: "Cap", Price: 2.25},
		{TraitType: "Hat", Value: "Crown", Price: 10},
	}

	got, matched := matchAttributeThreshold(attributes, thresholds)
	if !matched {
		t.Fatal("matchAttributeThreshold matched = false, want true")
	}
	want := tonToNano(2.25)
	if got != want {
		t.Fatalf("matchAttributeThreshold() = %v, want %v", got, want)
	}
}

func TestMatchAttributeThresholdNoMatch(t *testing.T) {
	attributes := []getgemsapi.NftItemAttribute{
		{TraitType: "Background", Value: "Blue"},
	}
	thresholds := []config.AttributePriceThreshold{
		{TraitType: "Background", Value: "Red", Price: 1.5},
	}

	got, matched := matchAttributeThreshold(attributes, thresholds)
	if matched {
		t.Fatal("matchAttributeThreshold matched = true, want false")
	}
	if got != 0 {
		t.Fatalf("matchAttributeThreshold() = %v, want 0", got)
	}
}
