package ios

import (
	"bytes"
	"context"
	"image/jpeg"
	"time"
)

// OCR's picture on the WebDriverAgent path. WDA's GET /screenshot is the
// screen at full size as a lossless PNG in base64, 1 to 15 MB, whatever
// its screenshotQuality says (it encodes the picture to PNG again). Over
// Wi-Fi, through the tunnel at about 1 MB/s, that took 3 to 25 s on an
// iPhone 13 mini (iOS 27.0.1, 2026-11-10), most of a read. A frame of
// WDA's MJPEG server is the screen at half size, JPEG at quality 50
// (wdaSettings): 20 to 300 KB. OCR finds the same lines in it: on the
// iPhone 17 Simulator (iOS 26.5) a Wikipedia page in Safari gave 35 lines
// from the frame and 39 from the screenshot, the same text but for a few
// words, mangled in both, of the page under Safari's tip.
//
// A new connection's first frame is taken after it connects (the server
// keeps none), so it shows the screen as it is now. Over a cable the
// screenshot stays: it is quick there, and sharper.

// ocrPicture returns the screen's picture for OCR, upright, and where it
// came from: "mjpeg" or "screenshot".
func (d *WDADevice) ocrPicture(ctx context.Context) ([]byte, string, error) {
	if b, ok := d.mjpegPicture(ctx); ok {
		return b, "mjpeg", nil
	}
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	png, err := d.c.screenshot(ctx)
	if err != nil {
		return nil, "screenshot", err
	}
	return d.uprightShot(ctx, png), "screenshot", nil
}

// mjpegPicture returns one frame of WDA's MJPEG server, upright, over
// Wi-Fi only. A frame that does not come within frameWait makes OCR use
// screenshots for frameRetry, as on the iOS 27 path (videoFrame).
func (d *WDADevice) mjpegPicture(ctx context.Context) ([]byte, bool) {
	if d.info.Connection != "wifi" || d.c.mjpeg == "" {
		return nil, false
	}
	if after := d.framesAfter.Load(); after != 0 && time.Now().UnixNano() < after {
		return nil, false
	}
	b, ok := d.mjpegFrame(ctx)
	if !ok && ctx.Err() == nil {
		d.framesAfter.Store(time.Now().Add(frameRetry).UnixNano())
	}
	return b, ok
}

func (d *WDADevice) mjpegFrame(ctx context.Context) ([]byte, bool) {
	upside := d.pictureTurn(ctx) // what the frame's EXIF does not say
	fctx, cancel := context.WithTimeout(ctx, frameWait)
	defer cancel()
	src, err := openMJPEG(fctx, d.c.long, d.c.mjpeg)
	if err != nil {
		return nil, false
	}
	defer src.Close()
	if r, ok := src.(*mjpegReader); ok {
		r.upside = func() quarter { return upside }
	}
	f, err := src.next(fctx)
	if err != nil {
		return nil, false
	}
	img, err := f.decode()
	if err != nil {
		return nil, false
	}
	if f.turn != 0 {
		img = turnImage(img, f.turn)
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 95}); err != nil {
		return nil, false
	}
	return buf.Bytes(), true
}
