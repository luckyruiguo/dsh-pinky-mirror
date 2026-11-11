package ios

import (
	"github.com/DIYgod/pinky/internal/ocr"
	"github.com/DIYgod/pinky/internal/screen"
)

// OCR is the shared engine in internal/ocr: Vision on macOS,
// Windows.Media.Ocr on Windows. These names keep the driver's code as it
// was before the engine moved there.

type ocrLine = ocr.Line

type ocrOptions = ocr.Options

// ocrLanguages are the languages OCR reads, in priority order.
var ocrLanguages = ocr.Languages

func recognizeText(img []byte, opt ocrOptions) ([]ocrLine, error) { return ocr.Recognize(img, opt) }

func ocrRect(l ocrLine, w, h int) screen.Rect { return ocr.Rect(l, w, h) }

// keyboardFromOCR finds an on-screen keyboard in OCR lines (ocr.KeyboardTop).
func keyboardFromOCR(lines []ocrLine) (float64, bool) { return ocr.KeyboardTop(lines) }

// keyboardSuggestions is how far above its top row of keys, as a part of
// the screen's height, the keyboard starts: its suggestions bar lies
// there, which OCR does not tell from the app.
const keyboardSuggestions = 0.06

// noteOCRKeyboard marks the keyboard as shown on s, whose keys' top row
// OCR found at top (normalized), with its frame unless the tree gave one.
func noteOCRKeyboard(s *screen.Screen, top float64) {
	s.Keyboard = true
	if !s.KeyboardFrame.Empty() || s.Width <= 0 || s.Height <= 0 {
		return
	}
	y := max(0, int((top-keyboardSuggestions)*float64(s.Height)))
	s.KeyboardFrame = screen.Rect{Y: y, W: s.Width, H: s.Height - y}
}

// ocrExtras returns the OCR lines no element already shows, as OCR
// elements (ocr.Extras).
func ocrExtras(els []screen.Element, lines []ocrLine, w, h int) []screen.Element {
	return ocr.Extras(els, lines, w, h)
}
