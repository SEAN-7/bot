package daemon

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/SEAN-7/bot/internal/proto"
)

// classify decides what an inbox file is. data is what was read, size the
// file's full size, path its full path (named in notes) and accepted the
// image media types the model accepts. A file of an accepted type that was
// read whole is shown as an image, bytes unchanged: what the provider does
// with an image it cannot take is its plugin's business. text means
// "handle as text"; otherwise content is the record's content, and img is
// set when the image is shown.
func classify(data []byte, size int64, path string, accepted []string) (text bool, content string, img *proto.Image) {
	typ := http.DetectContentType(data)
	if slices.Contains(accepted, typ) && int64(len(data)) == size {
		return false, fmt.Sprintf(MsgImageShownFmt, typ, len(data)), &proto.Image{MediaType: typ, Data: data}
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
