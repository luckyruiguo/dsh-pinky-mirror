//go:build windows

package ios

import (
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/video"
)

// TestEncoderLag measures how far behind the phone the encoder's output
// runs. Each picture carries its own number in eight blocks of luma at the
// top left, and a decoder reads the number back, so every picture coming
// out says which picture it came from. The test also logs the time the
// encoder stamped each packet with, which must be the time the picture was
// taken and not the moment the encoder gave it back.
//
// PINKY_H264_FPS and PINKY_H264_BITRATE change the stream it measures.
func TestEncoderLag(t *testing.T) {
	const (
		w, h   = 320, 240
		frames = 45
	)
	fps := 15
	if s := os.Getenv("PINKY_H264_FPS"); s != "" {
		fps, _ = strconv.Atoi(s)
	}
	bitrate := 500_000
	if s := os.Getenv("PINKY_H264_BITRATE"); s != "" {
		bitrate, _ = strconv.Atoi(s)
	}
	enc, err := newH264Encoder(w, h, fps, bitrate)
	if err != nil {
		t.Skipf("no encoder: %v", err)
	}
	name := enc.encoderName()
	enc.Close()
	t.Logf("encoder: %s, %d fps, %d bit/s", name, fps, bitrate)

	makePicture := func(id int) (y, uv []byte) {
		y = make([]byte, w*h)
		uv = make([]byte, (w/2)*h) // NV12: the chroma is interleaved, half the luma
		for bit := 0; bit < 8; bit++ {
			v := byte(16)
			if id&(1<<bit) != 0 {
				v = 235
			}
			for row := 0; row < 32; row++ {
				for col := bit * 32; col < (bit+1)*32; col++ {
					y[row*w+col] = v
				}
			}
		}
		return y, uv
	}
	readNumber := func(d *video.Decoder) int {
		var out video.YUV
		if err := d.ConvertYUV(&out); err != nil {
			t.Fatal(err)
		}
		id := 0
		for bit := 0; bit < 8; bit++ {
			p := out.Planes[0][16*out.Strides[0]+bit*32+16]
			if p > 128 {
				id |= 1 << bit
			}
		}
		return id
	}

	// run feeds the pictures as often as the phone would: gap apart, or as
	// fast as it can when gap is zero, which is a swipe.
	run := func(name string, gap time.Duration) {
		t.Logf("--- %s ---", name)
		enc, err := newH264Encoder(w, h, fps, bitrate)
		if err != nil {
			t.Fatal(err)
		}
		defer enc.Close()
		dec, err := video.NewDecoder(driver.H264, video.DecoderOptions{Software: true})
		if err != nil {
			t.Fatal(err)
		}
		defer dec.Close()
		start := time.Now()
		firstOut := -1
		for i := 0; i < frames; i++ {
			y, uv := makePicture(i)
			taken := time.Duration(i) * time.Second / time.Duration(fps)
			out, err := enc.encode(y, uv, taken, i%15 == 0)
			if err != nil {
				t.Fatal(err)
			}
			stamps := enc.packetTimes()
			if len(out) > 0 && firstOut < 0 {
				firstOut = i
			}
			if i < 3 || i == frames-1 {
				t.Logf("encoder: input %2d -> %d packets back", i, len(out))
			}
			for j, e := range out {
				if len(e.config) > 0 {
					if err := dec.Send(driver.Packet{Data: e.config, Config: true}); err != nil {
						t.Fatal(err)
					}
				}
				if err := dec.Send(driver.Packet{Data: e.data, KeyFrame: e.keyFrame}); err != nil {
					t.Fatal(err)
				}
				for {
					_, ok, err := dec.Receive()
					if err != nil {
						t.Fatal(err)
					}
					if !ok {
						break
					}
					id := readNumber(dec)
					var stamped time.Duration
					if j < len(stamps) {
						stamped = stamps[j]
					}
					t.Logf("at %6.0f ms: input %2d -> output carries %2d (lag %d pictures, stamped %.0f ms, picture taken at %.0f ms)",
						ms(time.Since(start)), i, id, i-id, ms(stamped), ms(taken))
				}
			}
			if gap > 0 {
				time.Sleep(gap)
			}
		}
		t.Logf("first packet back at input %d", firstOut)
		// What the encoder still holds is flushed, and dropped, by Close:
		// the stream is ending, so no one waits for those pictures.
	}

	run("steady", time.Second/time.Duration(fps))
	run("burst, no wait (a swipe)", 0)
}

// ms is a duration in milliseconds, for the log.
func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }
