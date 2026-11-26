package forwarding

import (
	"encoding/hex"
	"testing"

	"github.com/pion/rtp/codecs"
	"github.com/pion/webrtc/v4"
)

func TestAV1SequenceRespectsEachReceiversAnswer(t *testing.T) {
	packet := func(sequence string) []byte {
		t.Helper()
		unit, err := hex.DecodeString(sequence)
		if err != nil {
			t.Fatal(err)
		}
		packets := (&codecs.AV1Payloader{}).Payload(1200, unit)
		if len(packets) != 1 || packets[0][0]&0x08 == 0 {
			t.Fatal("expected the start of a coded sequence")
		}
		return packets[0]
	}
	// Sequence headers only: the 256-square fixture and the 1080p60 MF sample.
	small := packet("0a0b0000000443fdfe010cc020")
	large := packet("0a0c0000004eabbfc3fe0010cc02")
	answer := func(fmtp string) *webrtc.SessionDescription {
		return &webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: "v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\ns=-\r\nt=0 0\r\n" +
			"m=video 9 UDP/TLS/RTP/SAVPF 104\r\na=recvonly\r\na=rtpmap:104 AV1/90000\r\na=fmtp:104 " + fmtp + "\r\n"}
	}
	format, err := av1SequenceFormat(large)
	if err != nil || format.level != 9 || format.tier != 1 {
		t.Fatalf("1080p60 sequence = %+v, %v", format, err)
	}
	delta := []byte{0x10, 0x30, 0}
	for _, fmtp := range []string{"profile=0", "level-idx=5;tier=1", "level-idx=9;tier=0"} {
		var limited av1Stream
		peer := answer(fmtp)
		if !limited.allow(peer, 104, 65532, small, false) ||
			limited.allow(peer, 104, 65533, large, false) ||
			limited.allow(peer, 104, 65534, delta, false) {
			t.Fatal("failed to isolate the incompatible sequence")
		}
		if !limited.allow(peer, 104, 65535, small, false) || !limited.allow(peer, 104, 0, delta, false) {
			t.Fatal("compatible sequence did not resume across the sequence-number wrap")
		}
		for _, rtx := range []bool{true, false} {
			if limited.allow(peer, 104, 65534, delta, rtx) || !limited.allow(peer, 104, 65535, small, rtx) {
				t.Fatal("retransmission crossed sequence admission boundary")
			}
		}
	}
	var capable av1Stream
	peer := answer("level-idx=9;tier=1")
	if capable.allow(peer, 104, 1, delta, false) || !capable.allow(peer, 104, 2, large, false) {
		t.Fatal("capable sibling must start at a sequence header")
	}
	reduced := answer("level-idx=5;tier=1")
	if capable.allow(reduced, 104, 2, large, true) || capable.allow(reduced, 104, 3, delta, false) ||
		!capable.allow(reduced, 104, 4, small, false) {
		t.Fatal("replacement answer did not take effect for delta, repair and recovery")
	}
}
