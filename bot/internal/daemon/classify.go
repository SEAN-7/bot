package daemon

import (
	"bytes"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"net/http"
	"slices"
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
// file's full size, path its full path (named in notes) and accepted the image
// media types the model accepts. An image is shown only if the model accepts
// its type and the daemon can check it (imageExt). text means "handle as
// text"; otherwise content is the record's content, and img is set when the
// image is shown.
func classify(data []byte, size int64, path string, accepted []string) (text bool, content string, img *shownImage) {
	typ := http.DetectContentType(data)
	if strings.HasPrefix(typ, "image/") {
		var shown []string
		for _, t := range accepted {
			if _, ok := imageExt[t]; ok {
				shown = append(shown, t)
			}
		}
		if len(shown) == 0 {
			return false, fmt.Sprintf(MsgImageNoModelFmt, path), nil
		}
		if !slices.Contains(shown, typ) {
			return false, fmt.Sprintf(MsgImageTypeFmt, typ, strings.Join(shown, ", "), path), nil
		}
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
		return false, fmt.Sprintf(MsgImageShownFmt, typ, w, h, len(data)), &shownImage{typ, w, h, data}
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
