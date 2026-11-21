package contracts

import "testing"

func TestDescribePropertyFile(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\n")
	got, err := DescribePropertyFile(FilePropertyImage, "主图.png", png, 1024)
	if err != nil || got != (FileSummary{Name: "主图.png", MediaType: "image/png", Size: 8}) {
		t.Fatalf("image summary = %#v, %v", got, err)
	}
	for _, tc := range []struct {
		kind FilePropertyKind
		name string
		data []byte
		max  int64
	}{
		{"other", "file", png, 1024},
		{FilePropertyAttachment, "../file", png, 1024},
		{FilePropertyAttachment, "file\r\n", png, 1024},
		{FilePropertyAttachment, "file", png, 7},
		{FilePropertyAttachment, "file", nil, 1024},
		{FilePropertyImage, "image.png", []byte("<svg></svg>"), 1024},
	} {
		if _, err := DescribePropertyFile(tc.kind, tc.name, tc.data, tc.max); err == nil {
			t.Errorf("accepted invalid input: %q, %q, %d", tc.kind, tc.name, tc.max)
		}
	}
}
