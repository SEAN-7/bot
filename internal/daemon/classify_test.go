package daemon

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color/palette"
	"image/gif"
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

// hugePNG is a small PNG whose header declares 100000x100000 px.
func hugePNG() []byte {
	b := encodePNG(4, 4)
	binary.BigEndian.PutUint32(b[16:], 100000)
	binary.BigEndian.PutUint32(b[20:], 100000)
	binary.BigEndian.PutUint32(b[29:], crc32.ChecksumIEEE(b[12:29]))
	return b
}

func encodeJPEG(w, h int) []byte {
	var b bytes.Buffer
	if err := jpeg.Encode(&b, image.NewRGBA(image.Rect(0, 0, w, h)), nil); err != nil {
		panic(err)
	}
	return b.Bytes()
}

func encodeGIF(w, h int) []byte {
	var b bytes.Buffer
	if err := gif.Encode(&b, image.NewPaletted(image.Rect(0, 0, w, h), palette.Plan9), nil); err != nil {
		panic(err)
	}
	return b.Bytes()
}

func TestClassify(t *testing.T) {
	const path = "/home/x/inbox/processed/f"
	webp := append([]byte("RIFF\x24\x00\x00\x00WEBPVP8 "), make([]byte, 32)...)
	tests := []struct {
		name   string
		data   []byte
		size   int64 // 0 means len(data)
		path   string
		images bool
		text   bool
		media  string // expected shown media type, empty when no image is shown
		w, h   int
		want   []string // substrings of the content
	}{
		{name: "PNG shown", data: encodePNG(40, 30), images: true, media: "image/png", w: 40, h: 30, want: []string{"[image/png 40x30, ", " bytes, shown below]"}},
		{name: "JPEG shown", data: encodeJPEG(20, 10), images: true, media: "image/jpeg", w: 20, h: 10},
		{name: "GIF shown", data: encodeGIF(5, 5), images: true, media: "image/gif", w: 5, h: 5},
		{name: "JPEG named png", data: encodeJPEG(8, 8), path: "/home/x/inbox/processed/photo.png", images: true, media: "image/jpeg", w: 8, h: 8},
		{name: "too large", data: encodePNG(4, 4), size: 5<<20 + 1, images: true, want: []string{"is over the 5 MiB limit", path}},
		{name: "too wide", data: encodePNG(2001, 10), images: true, want: []string{"2001x10 is over 2000 px", path}},
		{name: "too tall", data: encodePNG(10, 2001), images: true, want: []string{"10x2001"}},
		{name: "huge declared size", data: hugePNG(), images: true, want: []string{"100000x100000 is over 2000 px"}},
		{name: "2000 px exactly", data: encodePNG(2000, 2000), images: true, media: "image/png", w: 2000, h: 2000},
		{name: "half-written JPEG", data: encodeJPEG(64, 64)[:40], images: true, want: []string{"could not decode it", path}},
		{name: "WebP", data: webp, images: true, want: []string{"image/webp is not supported", path}},
		{name: "no-image model", data: encodePNG(4, 4), images: false, want: []string{"does not accept images", path}},
		{name: "binary", data: []byte{0xff, 0x00, 0x01, 0x02}, images: true, want: []string{"[binary file not shown:", "4 bytes", path}},
		{name: "Latin-1 text", data: []byte("caf\xe9 au lait"), images: true, want: []string{"[binary file not shown:"}},
		{name: "UTF-8 text", data: []byte("héllo"), images: true, text: true},
		{name: "rune cut by read limit", data: []byte("héllo")[:2], size: 100, images: true, text: true},
		{name: "invalid byte before the cut", data: []byte("a\xffb\xc3"), size: 100, images: true, want: []string{"[binary file not shown:"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			size := tc.size
			if size == 0 {
				size = int64(len(tc.data))
			}
			p := tc.path
			if p == "" {
				p = path
			}
			text, content, img := classify(tc.data, size, p, tc.images)
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
				if img.MediaType != tc.media || img.Width != tc.w || img.Height != tc.h {
					t.Errorf("img = %s %dx%d, want %s %dx%d", img.MediaType, img.Width, img.Height, tc.media, tc.w, tc.h)
				}
				if !bytes.Equal(img.Data, tc.data) {
					t.Error("img.Data differs from the input")
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
