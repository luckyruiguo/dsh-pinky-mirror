package ios

import (
	"bytes"
	"compress/zlib"
	"encoding/hex"
	"io"
	"testing"

	"github.com/google/uuid"
	"howett.net/plist"
)

// capturedBlob is the uncompressed media blob of an offer Xcode sent to a
// device (from go-ios's offer_test.go), with session id 2368635137, LTRP on,
// FEC off and Xcode's "FLS;VRAE:0;SW:1;" feature list.
const capturedBlob = "080110012a7f088182bae90810001a3f087b120a0801100118c387032000120a" +
	"0801100218c387032000120a0801100118c387032000120a0801100218c38703" +
	"20001a09464c533b53573a313b20011a2e0864120a0801100118c38703200012" +
	"0a0801100218c3870320001a10464c533b565241453a303b53573a313b200e38" +
	"01403f6001320d56696365726f7920312e372e3040004a0908ea1f1000188080" +
	"014a0b080010c0d1e123188080204a0a08001080b489131880604a0508101084" +
	"204a0b08001080dac409188080064a05080410e4324a0b080010809bee021880" +
	"80084a0b08001080c2d72f188080404a0b080010808ece1c188080104a050801" +
	"10ab026880c0dd87d2a0c0e9ed017002800100900101"

func TestMediaBlobMatchesCapture(t *testing.T) {
	o := offerOptions{Features: "FLS;VRAE:0;SW:1;", LTRP: true, NoFEC: true}
	got := hex.EncodeToString(mediaBlob(2368635137, o))
	if got != capturedBlob {
		t.Fatalf("media blob drifted from the capture:\n got %s\nwant %s", got, capturedBlob)
	}
}

func TestOfferEnvelope(t *testing.T) {
	data, err := buildOffer(uuid.MustParse("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"), 7, defaultOfferOptions())
	if err != nil {
		t.Fatal(err)
	}
	var off struct {
		Info []byte `plist:"avcMediaStreamOptionRemoteEndpointInfo"`
		Mode int    `plist:"avcMediaStreamNegotiatorMode"`
		Blob []byte `plist:"avcMediaStreamNegotiatorMediaBlob"`
		Call string `plist:"avcMediaStreamOptionCallID"`
	}
	if _, err := plist.Unmarshal(data, &off); err != nil {
		t.Fatal(err)
	}
	if off.Mode != negotiatorModeVideo || off.Call != "AAAAAAAA-BBBB-CCCC-DDDD-EEEEEEEEEEEE" || len(off.Info) == 0 {
		t.Fatalf("bad envelope: %+v", off)
	}
	r, err := zlib.NewReader(bytes.NewReader(off.Blob))
	if err != nil {
		t.Fatal(err)
	}
	blob, _ := io.ReadAll(r)
	if !bytes.Equal(blob, mediaBlob(7, defaultOfferOptions())) {
		t.Fatal("blob does not round-trip")
	}
	if !bytes.Contains(blob, []byte("FLS;SW:1;")) || bytes.Contains(blob, []byte("VRAE")) {
		t.Fatal("default offer must not forbid resolution adaptation")
	}
}
