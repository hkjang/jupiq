package store

import "testing"

func TestPageBoundsClampsPageAndSize(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		page, pageSize           int
		wantPage, wantSize, want int
	}{
		{"defaults", 1, 20, 1, 20, 0},
		{"second page offsets by one page", 2, 20, 2, 20, 20},
		{"page below one falls back to the first", 0, 20, 1, 20, 0},
		{"negative page falls back to the first", -5, 20, 1, 20, 0},
		{"size below one falls back to twenty", 3, 0, 3, 20, 40},
		{"negative size falls back to twenty", 3, -1, 3, 20, 40},
		{"size above the cap is trimmed", 2, 5000, 2, 200, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			page, pageSize, offset := pageBounds(tc.page, tc.pageSize)
			if page != tc.wantPage || pageSize != tc.wantSize || offset != tc.want {
				t.Fatalf("pageBounds(%d, %d) = (%d, %d, %d), want (%d, %d, %d)",
					tc.page, tc.pageSize, page, pageSize, offset, tc.wantPage, tc.wantSize, tc.want)
			}
		})
	}
}

// Page numbers come straight from a query string, so a caller can ask for one
// large enough that page*pageSize wraps. A negative OFFSET is rejected by
// PostgreSQL, which the caller would see only as an opaque 500.
func TestPageBoundsNeverProducesNegativeOffset(t *testing.T) {
	for _, pageSize := range []int{-1, 0, 1, 20, 50, 200, 5000} {
		for _, page := range []int{maxInt, maxInt - 1, maxInt/2 + 1, maxInt / 3, 1 << 40} {
			gotPage, gotSize, offset := pageBounds(page, pageSize)
			if offset < 0 {
				t.Fatalf("pageBounds(%d, %d) offset = %d, want a non-negative offset", page, pageSize, offset)
			}
			if gotPage < 1 {
				t.Fatalf("pageBounds(%d, %d) page = %d, want at least 1", page, pageSize, gotPage)
			}
			if want := (gotPage - 1) * gotSize; offset != want {
				t.Fatalf("pageBounds(%d, %d) offset = %d, want %d for the clamped page", page, pageSize, offset, want)
			}
		}
	}
}

// The clamp must only engage past the overflow boundary: a page number that
// still fits has to be honoured exactly, or deep pagination would silently
// return the wrong slice.
func TestPageBoundsKeepsTheLargestPageThatFits(t *testing.T) {
	// pageSize 1 is excluded: its boundary page is maxInt itself, so there is
	// no representable page number above it to clamp.
	for _, pageSize := range []int{20, 50, 200} {
		page := maxInt / pageSize
		gotPage, _, offset := pageBounds(page, pageSize)
		if gotPage != page {
			t.Fatalf("pageBounds(%d, %d) page = %d, want it kept", page, pageSize, gotPage)
		}
		if offset != (page-1)*pageSize {
			t.Fatalf("pageBounds(%d, %d) offset = %d, want %d", page, pageSize, offset, (page-1)*pageSize)
		}
		if _, _, next := pageBounds(page+1, pageSize); next != offset {
			t.Fatalf("pageBounds(%d, %d) offset = %d, want it clamped to %d", page+1, pageSize, next, offset)
		}
	}
}
