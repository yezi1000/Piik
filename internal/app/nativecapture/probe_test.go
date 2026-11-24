package nativecapture

import (
	"strings"
	"testing"
)

const validProbe = `{"protocol":7,"platform":"windows","platformBuild":"26200","videoCapture":true,"processAudio":true,"systemAudio":true,"softwareVP8":true,"adapters":[{"index":0,"name":"GPU","identity":"0x0:0x1","hardwareH264":[{"index":0,"name":"H264","identity":"{encoder}"}]}]}`

func TestDecodeProbeSeparatesCaptureAudioAndHardwareEncode(t *testing.T) {
	capabilities, err := decodeProbe([]byte(validProbe))
	if err != nil {
		t.Fatal(err)
	}
	summary := capabilities.Summary()
	if !summary.Video || !summary.ProcessAudio || !summary.SystemAudio ||
		!summary.HardwareH264 || !summary.SoftwareVP8 {
		t.Fatalf("summary = %+v", summary)
	}
}

func TestDecodeProbeRejectsUnknownTrailingAndDuplicateData(t *testing.T) {
	tests := []string{
		strings.Replace(validProbe, `"protocol":7`, `"protocol":7,"extra":true`, 1),
		strings.Replace(validProbe, `"protocol":7`, `"protocol":6`, 1),
		validProbe + `{}`,
		strings.Replace(validProbe, `"adapters":[`, `"adapters":[{"index":0,"name":"GPU 2","identity":"0x0:0x2","hardwareH264":[]},`, 1),
	}
	for _, payload := range tests {
		if _, err := decodeProbe([]byte(payload)); err == nil {
			t.Fatalf("invalid probe was accepted: %s", payload)
		}
	}
}

func TestAudioExclusionCapabilityRequiresExplicitSupportAndMixing(t *testing.T) {
	capabilities, err := decodeProbe([]byte(validProbe))
	if err != nil || capabilities.Summary().ProcessAudioExclusion {
		t.Fatal("old capture probe advertised audio exclusion", err)
	}
	capabilities, err = decodeProbe([]byte(strings.Replace(validProbe, `"processAudio":true`,
		`"processAudio":true,"processAudioExclusion":true,"microphone":true`, 1)))
	if err != nil || !capabilities.Summary().ProcessAudioExclusion {
		t.Fatal("explicit audio exclusion capability was lost", err)
	}
	capabilities.Microphone = false
	if capabilities.Summary().ProcessAudioExclusion {
		t.Fatal("audio exclusion requires the replaceable mixed source")
	}
}

func TestSummaryDoesNotConflateVideoAudioAndEncoder(t *testing.T) {
	capabilities, err := decodeProbe([]byte(
		`{"protocol":7,"platform":"windows","platformBuild":"19045","videoCapture":true,"processAudio":false,"systemAudio":true,"adapters":[{"index":0,"name":"GPU","identity":"0x0:0x1","hardwareH264":[]}]}`,
	))
	if err != nil {
		t.Fatal(err)
	}
	summary := capabilities.Summary()
	if !summary.Video || summary.ProcessAudio || !summary.SystemAudio ||
		summary.HardwareH264 || summary.CaptureBorderControl {
		t.Fatalf("summary = %+v", summary)
	}
}

func TestAV1ProbeKeepsIndependentEncoderInventories(t *testing.T) {
	payload := strings.Replace(validProbe, `"adapters":[`, `"adapters":[{"index":1,"name":"AV1 GPU","identity":"0x0:0x2","hardwareH264":[],"hardwareAV1":[{"index":0,"name":"AV1","identity":"{av1}"}]},`, 1)
	capabilities, err := decodeProbe([]byte(payload))
	if err != nil {
		t.Fatal(err)
	}
	if summary := capabilities.Summary(); !summary.HardwareAV1 || !summary.HardwareH264 {
		t.Fatalf("one adapter's encoder inventory hid another: %+v", summary)
	}
	duplicate := strings.Replace(payload, `"hardwareAV1":[`, `"hardwareAV1":[{"index":0,"name":"duplicate","identity":"{duplicate}"},`, 1)
	if _, err := decodeProbe([]byte(duplicate)); err == nil {
		t.Fatal("duplicate AV1 encoder index was accepted")
	}
	if summary, _ := decodeProbe([]byte(validProbe)); summary.Summary().HardwareAV1 {
		t.Fatal("legacy probe invented AV1 support")
	}
}

func TestCaptureBorderCapabilityRequiresCaptureAndExplicitSupport(t *testing.T) {
	for _, supported := range []bool{false, true} {
		payload := validProbe
		if supported {
			payload = strings.Replace(payload, `"videoCapture":true`, `"videoCapture":true,"captureBorderControl":true`, 1)
		}
		capabilities, err := decodeProbe([]byte(payload))
		if err != nil || capabilities.Summary().CaptureBorderControl != supported {
			t.Fatalf("capture border support = %v, error = %v", capabilities.Summary(), err)
		}
		capabilities.VideoCapture = false
		if capabilities.Summary().CaptureBorderControl {
			t.Fatal("border control was advertised without video capture")
		}
	}
}
