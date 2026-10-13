package daemon

import (
	"bytes"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"net/http"
	"strings"
	"unicode/utf8"
)

const (
	imageMaxBytes = 5 << 20 // by the file's full size
	imageMaxEdge  = 2000    // px, each side
)

// shownImage is an inbox image the model will see.
type shownImage struct {
	MediaType     string
	Width, Height int
	Data          []byte
}

// classify decides what an inbox file is. data is what was read, size the
// file's full size, path its full path (named in notes) and images whether the
// model accepts images. text means "handle as text"; otherwise content is the
// record's content, and img is set when the image is shown.
func classify(data []byte, size int64, path string, images bool) (text bool, content string, img *shownImage) {
	typ := http.DetectContentType(data)
	switch typ {
	case "image/jpeg", "image/png", "image/gif":
		if size > imageMaxBytes {
			return false, fmt.Sprintf(MsgImageTooLargeFmt, size, path), nil
		}
		// Check the declared size first: Decode allocates it before reading pixels.
		cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
		if err != nil {
			return false, fmt.Sprintf(MsgImageUndecodableFmt, err, path), nil
		}
		w, h := cfg.Width, cfg.Height
		if w > imageMaxEdge || h > imageMaxEdge {
			return false, fmt.Sprintf(MsgImageTooWideFmt, w, h, path), nil
		}
		if _, _, err := image.Decode(bytes.NewReader(data)); err != nil {
			return false, fmt.Sprintf(MsgImageUndecodableFmt, err, path), nil
		}
		if !images {
			return false, fmt.Sprintf(MsgImageNoModelFmt, path), nil
		}
		return false, fmt.Sprintf(MsgImageShownFmt, typ, w, h, len(data)), &shownImage{typ, w, h, data}
	}
	if strings.HasPrefix(typ, "image/") {
		return false, fmt.Sprintf(MsgImageTypeFmt, typ, path), nil
	}
	check := data
	if int64(len(data)) < size {
		// The read limit may have cut a rune in two.
		for i := len(check) - 1; i >= 0 && i >= len(check)-3; i-- {
			if utf8.RuneStart(check[i]) {
				if !utf8.FullRune(check[i:]) {
					check = check[:i]
				}
				break
			}
		}
	}
	if !utf8.Valid(check) {
		if strings.HasPrefix(typ, "text/") { // e.g. Latin-1 text
			typ = "not valid UTF-8 text"
		}
		return false, fmt.Sprintf(MsgBinaryFmt, typ, size, path), nil
	}
	return true, "", nil
}
