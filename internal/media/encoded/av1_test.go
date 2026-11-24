package encoded

import (
	"encoding/hex"
	"testing"
)

// Generated from a solid-blue 256x256 NVENC AV1 frame, not user media.
const av1BlueFrame = "12000a0b0000000443fdfe010cc02032251001220108208201400040080120182008555a6e825d3d0071869b9cba2dc5719896377aa8"

func TestInspectAV1(t *testing.T) {
	frame, err := hex.DecodeString(av1BlueFrame)
	if err != nil {
		t.Fatal(err)
	}
	sequence, err := InspectAV1(frame)
	if err != nil || sequence == nil || sequence.MaxWidth != 256 || sequence.MaxHeight != 256 {
		t.Fatalf("sequence = %+v, %v", sequence, err)
	}
	// A delta temporal unit may omit configuration; an absent header is not
	// a zero-sized picture or an independent recovery proof.
	if sequence, err := InspectAV1([]byte{0x12, 0}); err != nil || sequence != nil {
		t.Fatalf("delimiter = %+v, %v", sequence, err)
	}
	for name, invalid := range map[string][]byte{
		"empty":              nil,
		"truncated sequence": frame[:8],
		"missing size":       {0x08, 0},
		"forbidden header":   {0x92, 0},
		"reserved bit":       {0x13, 0},
		"overlong length":    {0x12, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 1},
		"missing payload":    {0x32, 4, 1},
		"spatial layer":      {0x36, 0x08, 0},
		"temporal layer":     {0x36, 0x20, 0},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := InspectAV1(invalid); err == nil {
				t.Fatal("invalid temporal unit was accepted")
			}
		})
	}
}
