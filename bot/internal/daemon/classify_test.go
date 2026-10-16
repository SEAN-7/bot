package daemon

import (
	"bytes"
	"image"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"
)

func encodePNG(w, h int) []byte {
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, w, h))); err != nil {
		panic(err)
	}
	return b.Bytes()
}

func encodeJPEG(w, h int) []byte {
	var b bytes.Buffer
	if err := jpeg.Encode(&b, image.NewRGBA(image.Rect(0, 0, w, h)), nil); err != nil {
		panic(err)
	}
	return b.Bytes()
}

// The harness detects a file's type and shows it as an image when the model
// accepts that type, bytes unchanged; limits and decoding are the plugin's.
func TestClassify(t *testing.T) {
	const path = "/home/x/inbox/processed/f"
	webp := append([]byte("RIFF\x24\x00\x00\x00WEBPVP8 "), make([]byte, 32)...)
	tests := []struct {
		name  string
		data  []byte
		size  int64    // 0 means len(data)
		types []string // nil means JPEG, PNG and GIF
		text  bool
		media string // expected shown media type, empty when no image is shown
		want  []string
	}{
		{name: "png accepted", data: encodePNG(40, 30), media: "image/png", want: []string{"[image/png, ", " bytes, shown below]"}},
		{name: "jpeg accepted", data: encodeJPEG(20, 10), media: "image/jpeg"},
		{name: "webp accepted is shown unchecked", data: webp, types: []string{"image/webp"}, media: "image/webp"},
		{name: "half-written jpeg is shown unchecked", data: encodeJPEG(64, 64)[:40], media: "image/jpeg"},
		{name: "png not accepted", data: encodePNG(4, 4), types: []string{"image/jpeg"}, want: []string{"[binary file not shown: image/png, ", path}},
		{name: "no-image model", data: encodePNG(4, 4), types: []string{}, want: []string{"[binary file not shown: image/png, "}},
		{name: "oversized image is binary note", data: encodePNG(4, 4), size: 16<<20 + 1, want: []string{"[binary file not shown: image/png, 16777217 bytes", path}},
		{name: "binary", data: []byte{0xff, 0x00, 0x01, 0x02}, want: []string{"[binary file not shown:", "4 bytes", path}},
		{name: "latin-1 text is binary", data: []byte("caf\xe9 au lait"), want: []string{"[binary file not shown: not valid UTF-8 text, 12 bytes."}},
		{name: "utf-8 text", data: []byte("héllo"), text: true},
		{name: "truncated rune at read limit is text", data: []byte("héllo")[:2], size: 100, text: true},
		{name: "invalid byte before the cut", data: []byte("a\xffb\xc3"), size: 100, want: []string{"[binary file not shown:"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			size := tc.size
			if size == 0 {
				size = int64(len(tc.data))
			}
			types := tc.types
			if types == nil {
				types = []string{"image/jpeg", "image/png", "image/gif"}
			}
			text, content, img := classify(tc.data, size, path, types)
			if text != tc.text {
				t.Fatalf("text = %v, want %v (content %q)", text, tc.text, content)
			}
			if tc.text && (content != "" || img != nil) {
				t.Errorf("text result carries content %q or image", content)
			}
			if tc.media == "" {
				if img != nil {
					t.Fatalf("image shown, want none: %+v", img)
				}
			} else {
				if img == nil {
					t.Fatalf("image not shown; content %q", content)
				}
				if img.MediaType != tc.media || !bytes.Equal(img.Data, tc.data) {
					t.Errorf("img = %s, %d bytes; want %s, %d bytes", img.MediaType, len(img.Data), tc.media, len(tc.data))
				}
			}
			for _, w := range tc.want {
				if !strings.Contains(content, w) {
					t.Errorf("content %q does not contain %q", content, w)
				}
			}
		})
	}
}
