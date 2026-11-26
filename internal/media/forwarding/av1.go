package forwarding

import (
	"encoding/binary"
	"errors"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/bluenviron/mediacommon/v2/pkg/codecs/av1"
	"github.com/pion/interceptor"
	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"
	"github.com/pion/rtp/codecs/av1/obu"
	"github.com/pion/sdp/v3"
	"github.com/pion/webrtc/v4"
)

// A forwarding receiver does not decode. Each outgoing connection checks its
// own decoder's answer against the sequence it actually sends.
const AV1ForwardingFMTP = "level-idx=31;profile=0;tier=1"

type av1Format struct {
	profile, level, tier int
}

func (f av1Format) accepts(sequence av1Format) bool {
	return sequence.profile <= f.profile && sequence.level <= f.level && sequence.tier <= f.tier
}

func parseAV1Format(fmtp string) (av1Format, error) {
	format := av1Format{level: 5} // AV1 RTP §7.2 defaults.
	for _, part := range strings.Split(fmtp, ";") {
		key, value, _ := strings.Cut(part, "=")
		var target *int
		var maximum int
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "profile":
			target, maximum = &format.profile, 2
		case "level-idx":
			target, maximum = &format.level, 31
		case "tier":
			target, maximum = &format.tier, 1
		default:
			continue
		}
		number, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil || number < 0 || number > maximum {
			return av1Format{}, errors.New("invalid AV1 receive parameters")
		}
		*target = number
	}
	return format, nil
}

func IsAV1MainFormat(fmtp string) bool {
	format, err := parseAV1Format(fmtp)
	return err == nil && format.profile == 0
}

func av1ReceiveFormat(answer *webrtc.SessionDescription, payload uint8) (av1Format, error) {
	var session sdp.SessionDescription
	if err := session.UnmarshalString(answer.SDP); err != nil {
		return av1Format{}, err
	}
	for _, media := range session.MediaDescriptions {
		_, bundleOnly := media.Attribute("bundle-only")
		if media.MediaName.Media != "video" || (media.MediaName.Port.Value == 0 && !bundleOnly) ||
			!slices.Contains(media.MediaName.Formats, strconv.Itoa(int(payload))) {
			continue
		}
		section := sdp.SessionDescription{MediaDescriptions: []*sdp.MediaDescription{media}}
		codec, err := section.GetCodecForPayloadType(payload)
		if err == nil && strings.EqualFold(codec.Name, "AV1") {
			return parseAV1Format(codec.Fmtp)
		}
	}
	return av1Format{}, errors.New("answer does not accept the AV1 payload type")
}

// Each Piik encoder starts a coded sequence with its sequence header. Pion
// splits large frame OBUs at the MTU; only the sequence header is inspected.
func av1SequenceFormat(payload []byte) (av1Format, error) {
	var packet codecs.AV1Packet
	if _, err := packet.Unmarshal(payload); err != nil {
		return av1Format{}, err
	}
	for i, element := range packet.OBUElements {
		header, err := obu.ParseOBUHeader(element)
		if err != nil {
			return av1Format{}, err
		}
		if header.Type != obu.OBUSequenceHeader {
			continue
		}
		if header.ExtensionHeader != nil || packet.Y && i == len(packet.OBUElements)-1 {
			break
		}
		var sequence av1.SequenceHeader
		if err := sequence.Unmarshal(element); err != nil {
			return av1Format{}, err
		}
		format := av1Format{profile: int(sequence.SeqProfile), level: int(sequence.SeqLevelIdx[0])}
		if sequence.SeqTier[0] {
			format.tier = 1
		}
		return format, nil
	}
	return av1Format{}, errors.New("AV1 sequence starts without a complete sequence header")
}

type av1Egress struct {
	interceptor.NoOp
	connection func() *webrtc.PeerConnection
}

func (e *av1Egress) NewInterceptor(string) (interceptor.Interceptor, error) { return e, nil }

func (e *av1Egress) BindLocalStream(info *interceptor.StreamInfo, next interceptor.RTPWriter) interceptor.RTPWriter {
	if !strings.EqualFold(info.MimeType, webrtc.MimeTypeAV1) {
		return next
	}
	var stream av1Stream
	var mu sync.Mutex
	return interceptor.RTPWriterFunc(func(h *rtp.Header, payload []byte, attrs interceptor.Attributes) (int, error) {
		mu.Lock()
		defer mu.Unlock()
		if len(payload) > 0 {
			seq := h.SequenceNumber
			repair := info.SSRCRetransmission != 0 && h.SSRC == info.SSRCRetransmission
			media := payload
			if repair {
				if len(media) < 3 {
					return 0, nil
				}
				seq, media = binary.BigEndian.Uint16(media), media[2:]
			}
			if !stream.allow(e.connection().CurrentRemoteDescription(), info.PayloadType, seq, media, repair) {
				return 0, nil
			}
		}
		return next.Write(h, payload, attrs)
	})
}

type av1Stream struct {
	answer     *webrtc.SessionDescription
	limit      av1Format
	validLimit bool
	current    av1Format
	active     bool
	latest     int64
	// LiveKit can repair with either the primary SSRC or RTX. Only replay
	// packets admitted in the current independently decodable sequence.
	admitted [2048]int64
}

func (s *av1Stream) allow(answer *webrtc.SessionDescription, pt uint8, seq uint16, payload []byte, repair bool) bool {
	if answer == nil {
		return false
	}
	if s.answer != answer {
		limit, err := av1ReceiveFormat(answer, pt)
		s.answer, s.limit, s.validLimit = answer, limit, err == nil
		if !s.validLimit || !limit.accepts(s.current) {
			s.active = false
		}
	}
	extended := s.latest + int64(int16(seq-uint16(s.latest)))
	if s.latest == 0 {
		extended = 65536 + int64(seq)
	}
	slot := &s.admitted[int(seq)%len(s.admitted)]
	if repair || extended <= s.latest {
		return s.active && extended > 0 && s.latest-extended < int64(len(s.admitted)) && *slot == extended
	}
	s.latest = extended
	if payload[0]&0x08 != 0 {
		format, err := av1SequenceFormat(payload)
		compatible := err == nil && s.validLimit && s.limit.accepts(format)
		if !compatible && (s.active || s.current != format) {
			slog.Warn("AV1 sequence paused for receiver", "requiredLevel", format.level, "receiverLevel", s.limit.level, "requiredTier", format.tier, "receiverTier", s.limit.tier, "error", err)
		}
		s.active = compatible
		s.current = format
		clear(s.admitted[:])
	}
	if s.active {
		*slot = extended
	}
	return s.active
}
