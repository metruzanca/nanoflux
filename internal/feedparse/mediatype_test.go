package feedparse

import (
	"context"
	"strings"
	"testing"
)

func TestMediaTypeCategories(t *testing.T) {
	longText := strings.Repeat("word ", mediaTextMinRunes)
	shortText := "a short one-liner"
	tests := []struct {
		name string
		item Item
		want []string
	}{
		{
			name: "video enclosure",
			item: Item{Enclosures: []Enclosure{{URL: "https://example.com/clip.mp4"}}},
			want: []string{CategoryTypeVideo},
		},
		{
			name: "hls enclosure",
			item: Item{Enclosures: []Enclosure{{URL: "https://example.com/stream.m3u8"}}},
			want: []string{CategoryTypeVideo},
		},
		{
			name: "declared video kind wins over mime",
			item: Item{Enclosures: []Enclosure{{URL: "https://example.com/x", MIMEType: "application/octet-stream", Kind: EnclosureKindVideo}}},
			want: []string{CategoryTypeVideo},
		},
		{
			name: "audio enclosure",
			item: Item{Enclosures: []Enclosure{{URL: "https://example.com/ep.mp3", MIMEType: "audio/mpeg"}}},
			want: []string{CategoryTypeAudio},
		},
		{
			name: "image enclosure",
			item: Item{Enclosures: []Enclosure{{URL: "https://example.com/photo.jpg", MIMEType: "image/jpeg"}}},
			want: []string{CategoryTypeImage},
		},
		{
			name: "direct media link",
			item: Item{Link: "https://example.com/podcast.mp3"},
			want: []string{CategoryTypeAudio},
		},
		{
			name: "summary image",
			item: Item{Summary: `<p>look <img src="https://example.com/a.jpg"></p>`},
			want: []string{CategoryTypeImage},
		},
		{
			name: "summary picture",
			item: Item{Summary: `<picture><source srcset="a.webp"><img src="a.jpg"></picture>`},
			want: []string{CategoryTypeImage},
		},
		{
			name: "summary video",
			item: Item{Summary: `<video src="https://example.com/a.mp4"></video>`},
			want: []string{CategoryTypeVideo},
		},
		{
			name: "summary audio",
			item: Item{Summary: `<audio src="https://example.com/a.mp3"></audio>`},
			want: []string{CategoryTypeAudio},
		},
		{
			name: "embedded player iframe",
			item: Item{Summary: `<iframe src="https://player.example/embed/1"></iframe>`},
			want: []string{CategoryTypeVideo},
		},
		{
			name: "long text",
			item: Item{Summary: "<p>" + longText + "</p>"},
			want: []string{CategoryTypeText},
		},
		{
			name: "short text",
			item: Item{Summary: "<p>" + shortText + "</p>"},
			want: nil,
		},
		{
			name: "image and long text",
			item: Item{Summary: `<p>` + longText + `</p><img src="a.jpg">`},
			want: []string{CategoryTypeImage, CategoryTypeText},
		},
		{
			name: "video and image",
			item: Item{
				Summary:    `<img src="a.jpg">`,
				Enclosures: []Enclosure{{URL: "https://example.com/clip.mp4"}},
			},
			want: []string{CategoryTypeImage, CategoryTypeVideo},
		},
		{
			name: "video and long text",
			item: Item{
				Summary:    `<p>` + longText + `</p><video src="a.mp4"></video>`,
				Enclosures: []Enclosure{{URL: "https://example.com/clip.mp4"}},
			},
			want: []string{CategoryTypeVideo, CategoryTypeText},
		},
		{
			name: "thumbnail alone is not an image post",
			item: Item{ImageURL: "https://example.com/thumb.jpg", Summary: "<p>hello</p>"},
			want: nil,
		},
		{
			name: "script text does not count as prose",
			item: Item{Summary: "<script>" + longText + "</script><style>" + longText + "</style>"},
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mediaTypeCategories(&tt.item)
			if len(got) != len(tt.want) {
				t.Fatalf("mediaTypeCategories = %v, want %v", got, tt.want)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Fatalf("mediaTypeCategories = %v, want %v", got, tt.want)
				}
			}
		})
	}
}

func TestApplyMediaTypeCategoriesPreservesAndDedupes(t *testing.T) {
	res := Result{Items: []Item{{
		Summary:    `<img src="a.jpg">`,
		Categories: []string{"R/Cats", "Type:Image"},
	}}}
	applyMediaTypeCategories(&res)
	got := res.Items[0].Categories
	want := []string{"r/cats", "type:image"}
	if len(got) != len(want) {
		t.Fatalf("categories = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("categories = %v, want %v", got, want)
		}
	}
}

func TestApplyMediaTypeCategoriesFoldsBareMediaLabels(t *testing.T) {
	res := Result{Items: []Item{{
		// A feed that publishes its post type as a category, with no media the
		// parser can see. The bare word becomes the reserved identifier.
		Categories: []string{"Image", "NSFW", "r/cats", "u/sam"},
	}}}
	applyMediaTypeCategories(&res)
	got := res.Items[0].Categories
	want := []string{CategoryTypeImage, "nsfw", "r/cats", "u/sam"}
	if len(got) != len(want) {
		t.Fatalf("categories = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("categories = %v, want %v", got, want)
		}
	}
}

// mediaPlugin is a Plugin that fetches a fixed result, so the test can assert
// the core media-type pass also runs on the plugin path.
type mediaPlugin struct{}

func (mediaPlugin) MatchFetch(FetchRequest) bool      { return true }
func (mediaPlugin) MatchSharedKeys(FetchRequest) bool { return false }
func (mediaPlugin) SharedKeysPlugin(context.Context, FetchRequest, *Result) error {
	return nil
}
func (mediaPlugin) FetchPlugin(context.Context, FetchRequest) (Result, error) {
	return Result{Items: []Item{{
		GUID:       "1",
		Link:       "https://example.com/1",
		Summary:    `<video src="https://example.com/clip.mp4"></video>`,
		Categories: []string{"u/poster"},
	}}}, nil
}

func TestPluginFetchedItemsGetMediaTypeCategories(t *testing.T) {
	SetPlugin(mediaPlugin{})
	defer SetPlugin(nil)

	res, err := FetchFeed(context.Background(), FetchRequest{URL: "https://example.com/feed"}, nil)
	if err != nil {
		t.Fatalf("FetchFeed: %v", err)
	}
	got := res.Items[0].Categories
	want := []string{"u/poster", CategoryTypeVideo}
	if len(got) != len(want) {
		t.Fatalf("categories = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("categories = %v, want %v", got, want)
		}
	}
}
