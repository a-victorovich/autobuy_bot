package monitor

import "testing"

func TestCalculateThreshold(t *testing.T) {
	tests := []struct {
		name        string
		floorPrice  float64
		discountPct float64
		want        float64
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
