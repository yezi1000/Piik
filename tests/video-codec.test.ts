import { expect, it, vi } from "vitest";
import { createScreenReceiveAnswer } from "../src/client/webrtc/screen-receive-answer";
import { normalizeVideoOfferSdp, requireReceivingVideo, supportsH264Receive, VideoReceiveRejectedError } from "../src/client/webrtc/video-codec";
import { applyVideoCodecPreference, manualVideoCodecPreference, supportsVideoReceive } from "../src/client/webrtc/video-codec";

it("raises only the AV1 receive level confirmed by WebRTC decodingInfo", async () => {
  const sdp = [
    "v=0", "o=- 1 1 IN IP4 127.0.0.1", "s=-", "t=0 0", "m=video 9 UDP/TLS/RTP/SAVPF 104 105",
    "a=recvonly", "a=rtpmap:104 AV1/90000", "a=fmtp:104 profile=0;x-level-idx=5;level-idx=5;tier=0",
    "a=rtpmap:105 rtx/90000", "a=fmtp:105 apt=104", "",
  ].join("\r\n");
  const answer = { type: "answer", sdp } as const;
  const pc = { createAnswer: async () => answer } as RTCPeerConnection;
  const decodingInfo = vi.fn(async ({ video }: MediaDecodingConfiguration) => ({
    supported: true, smooth: video?.width === 2048,
  }));
  vi.stubGlobal("navigator", { mediaCapabilities: { decodingInfo } });
  try {
    const upgraded = await createScreenReceiveAnswer(pc);
    expect(upgraded.sdp).toContain("x-level-idx=5;level-idx=9;tier=0");
    expect(upgraded.sdp).toContain("a=fmtp:105 apt=104");
    expect(decodingInfo.mock.calls.map(([query]) => query.type)).toEqual(["webrtc", "webrtc", "webrtc", "webrtc"]);
    decodingInfo.mockResolvedValue({ supported: true, smooth: true });
    expect((await createScreenReceiveAnswer(pc)).sdp).toContain("x-level-idx=5;level-idx=14;tier=1");
    decodingInfo.mockResolvedValue({ supported: false, smooth: true });
    expect(await createScreenReceiveAnswer(pc)).toEqual(answer);
    decodingInfo.mockRejectedValue(new TypeError("unsupported configuration"));
    expect(await createScreenReceiveAnswer(pc)).toEqual(answer);
  } finally {
    vi.unstubAllGlobals();
  }
});

it("selects AV1 Main with repair and keeps manual AV1 strict", () => {
  const main = { mimeType: "video/AV1", clockRate: 90_000, sdpFmtpLine: "profile=0;tier=0" };
  const high = { ...main, sdpFmtpLine: "profile=1;tier=0" };
  const vp8 = { mimeType: "video/VP8", clockRate: 90_000 };
  const rtx = { mimeType: "video/rtx", clockRate: 90_000 };
  const codecs = [vp8, high, main, rtx];
  const setCodecPreferences = vi.fn();
  vi.stubGlobal("RTCRtpSender", { getCapabilities: () => ({ codecs }) });
  vi.stubGlobal("RTCRtpReceiver", { getCapabilities: () => ({ codecs: [main] }) });
  try {
    expect(applyVideoCodecPreference({ setCodecPreferences } as unknown as RTCRtpTransceiver,
      manualVideoCodecPreference("av1"))).toBe(true);
    expect(setCodecPreferences).toHaveBeenCalledWith([main, rtx]);
    expect(supportsVideoReceive("av1")).toBe(true);
    codecs.splice(0, codecs.length, vp8, high, rtx);
    expect(applyVideoCodecPreference({ setCodecPreferences } as unknown as RTCRtpTransceiver,
      manualVideoCodecPreference("av1"))).toBe(false);
    expect(setCodecPreferences).toHaveBeenCalledTimes(1);
    vi.stubGlobal("RTCRtpReceiver", { getCapabilities: () => ({ codecs: [high] }) });
    expect(supportsVideoReceive("av1")).toBe(false);
  } finally {
    vi.unstubAllGlobals();
  }
});

it("checks H264 receive capability independently of sender support", () => {
  const h264 = { mimeType: "video/H264", clockRate: 90_000, sdpFmtpLine: "packetization-mode=1" };
  const receive = vi.fn(() => ({ codecs: [h264] }));
  vi.stubGlobal("RTCRtpSender", { getCapabilities: () => ({ codecs: [h264] }) });
  vi.stubGlobal("RTCRtpReceiver", { getCapabilities: receive });
  try {
    expect(supportsH264Receive()).toBe(true);
    expect(receive).toHaveBeenCalledWith("video");
    receive.mockReturnValue({ codecs: [{ ...h264, sdpFmtpLine: "packetization-mode=0" }] });
    expect(supportsH264Receive()).toBe(false);
    receive.mockReturnValue({ codecs: [] });
    expect(supportsH264Receive()).toBe(false);
    vi.stubGlobal("RTCRtpReceiver", undefined);
    expect(supportsH264Receive()).toBe(false);
  } finally {
    vi.unstubAllGlobals();
  }
});

it("recognizes a Browser-rejected video answer without inferring codec support", () => {
  const offer = (sdp: string): RTCSessionDescriptionInit => ({ type: "offer", sdp });
  const answer = (sdp: string): RTCSessionDescriptionInit => ({ type: "answer", sdp });
  const video = "m=video 9 UDP/TLS/RTP/SAVPF 96\r\na=sendonly\r\na=rtpmap:96 H264/90000\r\n";
  const rejected = "m=video 0 UDP/TLS/RTP/SAVPF 120\r\n";
  expect(() => requireReceivingVideo(offer(video), answer(rejected))).toThrow(VideoReceiveRejectedError);
  for (const [offered, accepted] of [
    [video, video.replace("sendonly", "recvonly")],
    [video + video, rejected + video.replace("sendonly", "recvonly")],
    [video.replace("9 UDP", "0 UDP"), rejected],
    [video.replace("sendonly", "inactive"), rejected],
    [video.replace("sendonly", "recvonly"), rejected],
    ["m=audio 9 UDP/TLS/RTP/SAVPF 111\r\n", "m=audio 0 UDP/TLS/RTP/SAVPF 111\r\n"],
  ]) expect(() => requireReceivingVideo(offer(offered!), answer(accepted!))).not.toThrow();
  expect(() => requireReceivingVideo(offer(video.replace("9 UDP", "0 UDP") + "a=bundle-only\r\n"),
    answer(rejected))).toThrow(VideoReceiveRejectedError);
});

it.each(["\r\n", "\n"])("removes orphan codec attributes within each offered video section (%j)", newline => {
  const lines = [
    "v=0", "a=x-session:keep",
    "m=video 9 UDP/TLS/RTP/SAVPF 120 124", "a=mid:0", "a=ice-ufrag:unchanged",
    "a=rtpmap:120 VP8/90000", "a=rtpmap:124 rtx/90000", "a=fmtp:124 apt=120",
    "a=rtcp-fb:120 nack", "a=rtcp-fb:* transport-cc", "a=x-video:keep",
    "a=fmtp:126 profile-level-id=42e01f", "a=rtcp-fb:126 nack pli",
    "m=video 9 UDP/TLS/RTP/SAVPF 126", "a=mid:1", "a=rtpmap:126 H264/90000",
    "a=fmtp:126 profile-level-id=42e01f;packetization-mode=1", "a=rtcp-fb:126 nack",
    "a=rtcp-fb:120 nack pli",
    "m=audio 9 UDP/TLS/RTP/SAVPF 0", "a=fmtp:0 keep-audio",
    "m=application 9 UDP/DTLS/SCTP webrtc-datachannel", "a=sctp-port:5000", "",
  ];
  const omitted = new Set([11, 12, 18]);
  const expected = lines.filter((_line, index) => !omitted.has(index)).join(newline);
  expect(normalizeVideoOfferSdp(lines.join(newline))).toBe(expected);
  expect(normalizeVideoOfferSdp(expected)).toBe(expected);
});

it("preserves valid codec parameters, feedback and opaque data without reserializing SDP", () => {
  const sdp = "v=0\r\nm=video 9 UDP/TLS/RTP/SAVPF 96 97\r\n" +
    "a=rtpmap:96 H264/90000\r\na=fmtp:96 profile-level-id=42e034;packetization-mode=1\r\n" +
    "a=rtpmap:97 rtx/90000\r\na=fmtp:97 apt=96\r\na=rtcp-fb:96 nack\r\n" +
    "a=rtcp-fb:* transport-cc\r\na=unknown:exact spacing  and values\r\n";
  expect(normalizeVideoOfferSdp(sdp)).toBe(sdp);
});
