package encoded

import (
	"errors"

	"github.com/bluenviron/mediacommon/v2/pkg/codecs/av1"
	"github.com/pion/rtp/codecs/av1/obu"
)

// AV1Sequence bounds the maximum frame dimensions declared by a temporal unit.
type AV1Sequence struct {
	MaxWidth, MaxHeight uint32
}

// InspectAV1 admits the single-layer, 8-bit Main profile used by native capture.
// Pion owns OBU framing and mediacommon owns sequence syntax; allocation bounds
// remain with the capture/decoder caller before any decoder is constructed.
func InspectAV1(data []byte) (*AV1Sequence, error) {
	if len(data) == 0 || len(data) > MaxAccessUnitBytes {
		return nil, errors.New("AV1 temporal unit size is invalid")
	}
	var sequence *AV1Sequence
	for len(data) > 0 {
		header, err := obu.ParseOBUHeader(data)
		if err != nil || !header.HasSizeField || header.Reserved1Bit {
			return nil, errors.New("AV1 OBU header is invalid")
		}
		if extension := header.ExtensionHeader; extension != nil &&
			(extension.SpatialID != 0 || extension.TemporalID != 0 || extension.Reserved3Bits != 0) {
			return nil, errors.New("AV1 layered input is unsupported")
		}
		payload := data[header.Size():]
		// AV1 leb128 is at most eight bytes. Bound the library reader as well
		// as the resulting slice before using untrusted lengths.
		size, length, err := obu.ReadLeb128(payload[:min(8, len(payload))])
		if err != nil || length > uint(len(payload)) || size > uint(len(payload))-length {
			return nil, errors.New("AV1 OBU size is invalid")
		}
		end := header.Size() + int(length+size)
		if header.Type == obu.OBUSequenceHeader {
			if header.ExtensionHeader != nil {
				return nil, errors.New("AV1 sequence header cannot have a layer extension")
			}
			var parsed av1.SequenceHeader
			if err := parsed.Unmarshal(data[:end]); err != nil {
				return nil, err
			}
			if parsed.SeqProfile != 0 || parsed.ColorConfig.BitDepth != 8 ||
				!parsed.ColorConfig.SubsamplingX || !parsed.ColorConfig.SubsamplingY ||
				parsed.OperatingPointsCntMinus1 != 0 {
				return nil, errors.New("AV1 capture requires single-layer 8-bit Main 4:2:0")
			}
			width, height := uint32(parsed.Width()), uint32(parsed.Height())
			if sequence != nil && (sequence.MaxWidth != width || sequence.MaxHeight != height) {
				return nil, errors.New("AV1 temporal unit changes decoder dimensions")
			}
			sequence = &AV1Sequence{MaxWidth: width, MaxHeight: height}
		}
		data = data[end:]
	}
	return sequence, nil
}
