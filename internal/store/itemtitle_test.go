package store

import "testing"

func TestItemDisplayTitle(t *testing.T) {
	tests := []struct {
		name string
		item Item
		want string
	}{
		{
			name: "stored title wins",
			item: Item{ID: 1, GUID: "external-id", Title: "A real headline"},
			want: "A real headline",
		},
		{
			name: "surrounding whitespace is trimmed",
			item: Item{ID: 1, GUID: "external-id", Title: "  A real headline  "},
			want: "A real headline",
		},
		{
			name: "empty title falls back to the external id",
			item: Item{ID: 40192, GUID: "832737045877309440"},
			want: "832737045877309440",
		},
		{
			name: "whitespace-only title falls back to the external id",
			item: Item{ID: 40192, GUID: "832737045877309440", Title: " \n\t "},
			want: "832737045877309440",
		},
		{
			name: "missing guid falls back to the identity",
			item: Item{ID: 7, Identity: "site:post_7"},
			want: "site:post_7",
		},
		{
			name: "no external id falls back to the local id",
			item: Item{ID: 7},
			want: "item 7",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.item.DisplayTitle(); got != tt.want {
				t.Errorf("DisplayTitle() = %q, want %q", got, tt.want)
			}
		})
	}
}
