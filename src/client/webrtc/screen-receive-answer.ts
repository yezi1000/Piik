import { parse, parsePayloads, write } from "sdp-transform";
import { preferScreenAudioStereo } from "./opus-receive-sdp";
import { isAV1MainFormat } from "./video-codec";

const defaultLevel = /(^|;)(\s*level-idx\s*=\s*)5(?=\s*(?:;|$))/i;
const defaultTier = /(^|;)(\s*tier\s*=\s*)0(?=\s*(?:;|$))/i;
const decodeConfigurations = [
  { level: 14, tier: 1, width: 4096, height: 2160, bitrate: 240_000_000, framerate: 120 },
  { level: 14, tier: 0, width: 4096, height: 2160, bitrate: 60_000_000, framerate: 120 },
  { level: 13, tier: 0, width: 4096, height: 2160, bitrate: 40_000_000, framerate: 60 },
  { level: 9, tier: 0, width: 2048, height: 1152, bitrate: 20_000_000, framerate: 60 },
] as const;

// Chromium can return the AV1 RTP default (3.1) even on a 4K decoder.
// Ask the WebRTC decoder for higher screen-sharing configurations.
// Keep its original answer when the API or a smooth configuration is unavailable.
export async function createScreenReceiveAnswer(pc: RTCPeerConnection): Promise<RTCSessionDescriptionInit> {
  const answer = preferScreenAudioStereo(await pc.createAnswer());
  const capabilities = globalThis.navigator?.mediaCapabilities;
  if (!answer.sdp || !capabilities?.decodingInfo || !/ AV1\/90000/i.test(answer.sdp)) return answer;
  const session = parse(answer.sdp);
  for (const section of session.media) {
    if (section.type !== "video" || section.port === 0 || section.direction === "sendonly" || section.direction === "inactive") continue;
    const payloads = parsePayloads(section.payloads ?? "");
    const codec = section.rtp.find(rtp => payloads.includes(rtp.payload) && rtp.codec.toLowerCase() === "av1");
    if (!codec) continue;
    const format = section.fmtp.find(fmtp => fmtp.payload === codec.payload);
    const config = format?.config ?? "";
    const hasLevel = /(?:^|;)\s*level-idx\s*=/i.test(config);
    if (!isAV1MainFormat(config) || /(?:^|;)\s*tier\s*=\s*1\b/i.test(config) || (hasLevel && !defaultLevel.test(config))) continue;
    try {
      for (const { level, tier, width, height, bitrate, framerate } of decodeConfigurations) {
        const result = await capabilities.decodingInfo({
          type: "webrtc",
          video: { contentType: `video/AV1;profile=0;tier=${tier};level-idx=${level}`, width, height, bitrate, framerate },
        });
        if (result.supported && result.smooth) {
          let updated = hasLevel ? config.replace(defaultLevel, (_, separator, key) => `${separator}${key}${level}`)
            : [config, `level-idx=${level}`].filter(Boolean).join(";");
          if (tier === 1) {
            updated = defaultTier.test(updated) ? updated.replace(defaultTier, (_, separator, key) => `${separator}${key}1`)
              : `${updated};tier=1`;
          }
          if (format) format.config = updated;
          else section.fmtp.push({ payload: codec.payload, config: updated });
          return { type: "answer", sdp: write(session) };
        }
      }
    } catch { return answer; }
  }
  return answer;
}
