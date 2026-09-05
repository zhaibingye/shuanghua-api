export const meta = {
  apiVersion: 1,
  key: "doubao",
  name: "Doubao Video",
  icon: "Doubao.Color",
  description: {
    en: "Seedance video generation. Use an Ark API key for ordinary generation, or ark_api_key|mediakit_api_key to enable MediaKit enhancement. Use task usage expressions to price tokens plus enhancement seconds; no enhancement price is added automatically.",
    zh: "Seedance 视频生成。普通生成填写 Ark API Key；填写 ark_api_key|mediakit_api_key 启用 MediaKit 超分。请使用任务用量表达式设置 token 与超分秒数单价，系统不会自动附加超分价格。",
  },
  version: "1.1.0",
  allowedHosts: ["amk.cn-beijing.volces.com"],
  author: { name: "QuantumNous" },
  channelTypes: [54, 45], // VolcEngine-type channels serve Ark video models with the same wire format
  models: [
    "doubao-seedance-1-0-pro-250528",
    "doubao-seedance-1-0-lite-t2v",
    "doubao-seedance-1-0-lite-i2v",
    "doubao-seedance-1-5-pro-251215",
    "doubao-seedance-2-0-260128",
    "doubao-seedance-2-0-fast-260128",
    "doubao-seedance-2-0-mini-260615",
    "doubao-seedance-2-5-260628",
  ],
  fetchMode: "per_task",
  usageSchema: {
    tokens: {
      type: "number",
      unit: "token",
      description: {
        en: "Upstream billing tokens (estimated at submit, actual on completion).",
        zh: "上游计费 token（提交时预估，完成后按实际值）。",
      },
    },
    resolution: {
      enum: ["480p", "720p", "1080p", "4k"],
      description: {
        en: "Output video resolution; Seedance token unit price varies by resolution tier.",
        zh: "输出视频分辨率；Seedance token 单价随分辨率档位变化。",
      },
    },
    enhancement_seconds: {
      type: "number",
      unit: "second",
      description: {
        en: "MediaKit enhancement duration; zero for ordinary Ark generation.",
        zh: "MediaKit 超分时长；普通 Ark 生成时为零。",
      },
    },
    enhancement_resolution: {
      enum: ["none", "720p", "1080p"],
      description: {
        en: "Final MediaKit resolution, separate from the Ark generation resolution.",
        zh: "MediaKit 最终输出分辨率，与 Ark 生成分辨率分别计费。",
      },
    },
    video_input: {
      enum: ["none", "video"],
      description: {
        en: "Whether the request includes reference video input; Seedance prices video-to-video tokens at a lower unit rate.",
        zh: "请求是否包含参考视频输入；Seedance 对视频生视频 token 按更低单价计费。",
      },
    },
  },
  // Official Ark formula tokens = (input + output seconds) × W × H × 24 / 1024,
  // 16:9 max-pixel sizes, cross-checked against Volcengine price examples.
  usageExamples: [
    { label: "480p · 5s", facts: { tokens: 48038, resolution: "480p", video_input: "none", enhancement_seconds: 0, enhancement_resolution: "none" } },
    { label: "720p · 5s", facts: { tokens: 108000, resolution: "720p", video_input: "none", enhancement_seconds: 0, enhancement_resolution: "none" } },
    { label: "1080p · 5s", facts: { tokens: 243000, resolution: "1080p", video_input: "none", enhancement_seconds: 0, enhancement_resolution: "none" } },
    { label: "4k · 5s", facts: { tokens: 972000, resolution: "4k", video_input: "none", enhancement_seconds: 0, enhancement_resolution: "none" } },
    { label: "720p · 10s", facts: { tokens: 216000, resolution: "720p", video_input: "none", enhancement_seconds: 0, enhancement_resolution: "none" } },
    { label: "720p · 5s (+4s input video)", facts: { tokens: 194400, resolution: "720p", video_input: "video", enhancement_seconds: 0, enhancement_resolution: "none" } },
    { label: "MediaKit · 480p → 720p · 5s", facts: { tokens: 48038, resolution: "480p", video_input: "none", enhancement_seconds: 5, enhancement_resolution: "720p" } },
    { label: "MediaKit · 480p → 1080p · 5s", facts: { tokens: 48038, resolution: "480p", video_input: "none", enhancement_seconds: 5, enhancement_resolution: "1080p" } },
    { label: "MediaKit · 720p → 1080p · 5s", facts: { tokens: 108000, resolution: "720p", video_input: "none", enhancement_seconds: 5, enhancement_resolution: "1080p" } },
  ],
  routes: [
    { method: "POST", path: "/doubao/api/v3/contents/generations/tasks", type: "submit", decode: "createTask", render: "taskCreated" },
    { method: "GET", path: "/doubao/api/v3/contents/generations/tasks/:task_id", type: "query", render: "taskStatus" },
  ],
  protocols: [{ name: "openai_responses", supports: ["stream", "sync", "background"] }, "openai_video"],
};

function trimmed(value) {
  return String(value || "").trim();
}

// These ceilings mirror the host-owned task usage units in Task Plugin API v1.
const MAX_SECONDS = 3600;
const MAX_COUNT = 128;
const MEDIAKIT_BASE_URL = "https://amk.cn-beijing.volces.com";

function channelCredentials(ctx) {
  const raw = trimmed(ctx.apiKey);
  let arkKey = raw;
  let mediaKey = "";
  let mediaBaseUrl = MEDIAKIT_BASE_URL;
  if (raw.startsWith("{")) {
    let parsed;
    try {
      parsed = JSON.parse(raw);
    } catch {
      throw new Error("Invalid Doubao channel credentials JSON");
    }
    arkKey = trimmed(parsed.ark_api_key);
    mediaKey = trimmed(parsed.mediakit_api_key);
    if (parsed.mediakit_base_url !== undefined) mediaBaseUrl = trimmed(parsed.mediakit_base_url);
    if (!mediaKey) throw new Error("Both Ark and MediaKit API keys are required for composite credentials");
  } else if (raw.includes("|")) {
    const parts = raw.split("|");
    if (parts.length !== 2) throw new Error("Use ark_api_key|mediakit_api_key for composite credentials");
    arkKey = trimmed(parts[0]);
    mediaKey = trimmed(parts[1]);
    if (!mediaKey) throw new Error("Both Ark and MediaKit API keys are required for composite credentials");
  }
  if (!arkKey || /[\r\n]/.test(arkKey + mediaKey)) throw new Error("Invalid Doubao channel credentials");
  if (mediaKey) {
    // Custom proxies are allowed only on the channel's own origin. Additional
    // hosts require a reviewed plugin override, not a client request parameter.
    const match = /^(https?):\/\/([^/?#@]+)(\/[^?#]*)?$/.exec(mediaBaseUrl);
    const base = /^(https?):\/\/([^/?#@]+)/.exec(trimmed(ctx.baseUrl));
    if (!match || (mediaBaseUrl !== MEDIAKIT_BASE_URL && (!base || match[1] !== base[1] || match[2].toLowerCase() !== base[2].toLowerCase()))) {
      throw new Error("MediaKit base URL must use the official endpoint or the channel origin");
    }
  }
  return { arkKey: arkKey, mediaKey: mediaKey, mediaBaseUrl: mediaBaseUrl.replace(/\/+$/, "") };
}

function boundedInteger(value, name, maximum) {
  if ((typeof value !== "number" && typeof value !== "string") || !String(value).trim()) throw new Error(name + " must be a positive integer");
  const number = Number(value);
  if (!Number.isInteger(number) || number < 1 || number > maximum) throw new Error(name + " must be between 1 and " + maximum);
  return number;
}

// One canonical upstream body supplies request conversion AND estimated facts.
// Channel credentials, never client metadata, select the enhancement pipeline.
function generationRequest(ctx) {
  const req = ctx.requestBody || {};
  if (req.metadata !== undefined && (!req.metadata || typeof req.metadata !== "object" || Array.isArray(req.metadata))) throw new Error("metadata must be an object");
  const metadata = req.metadata || {};
  const body = Object.assign({}, metadata);
  for (const key of ["content", "callback_url", "output_format", "return_last_frame", "service_tier", "execution_expires_after", "generate_audio", "draft", "tools", "safety_identifier", "priority", "resolution", "ratio", "frames", "seed", "camera_fixed", "watermark"]) {
    if (req[key] !== undefined) body[key] = req[key];
  }
  for (const key of ["return_last_frame", "generate_audio", "draft", "camera_fixed", "watermark"]) {
    if (body[key] === undefined) continue;
    if (body[key] === "true") body[key] = true;
    if (body[key] === "false") body[key] = false;
    if (typeof body[key] !== "boolean") throw new Error(key + " must be a boolean");
  }
  if (body.seed !== undefined) {
    const seed = Number(body.seed);
    if ((typeof body.seed !== "number" && typeof body.seed !== "string") || !String(body.seed).trim() || !Number.isInteger(seed) || seed < -1 || seed > 2147483647) throw new Error("seed must be an integer between -1 and 2147483647");
    body.seed = seed;
  }
  // Validate even shadowed aliases: metadata and multipart must not bypass bounds.
  for (const source of [req, metadata]) {
    for (const key of ["seconds", "duration"]) {
      if (source[key] !== undefined) boundedInteger(source[key], key, MAX_SECONDS);
    }
    if (source.frames !== undefined) boundedInteger(source.frames, "frames", MAX_SECONDS * 24);
    if (source.n !== undefined) boundedInteger(source.n, "n", MAX_COUNT);
  }
  const duration = req.seconds ?? req.duration ?? metadata.duration ?? metadata.seconds;
  let seconds = duration === undefined ? 5 : boundedInteger(duration, "duration", MAX_SECONDS);
  if (body.frames !== undefined) {
    if (duration !== undefined) throw new Error("Specify duration or frames, not both");
    body.frames = boundedInteger(body.frames, "frames", MAX_SECONDS * 24);
    seconds = body.frames / 24;
    delete body.duration;
  } else {
    body.duration = seconds;
  }
  delete body.seconds;
  const rawResolution = body.resolution ?? req.size ?? "720p";
  const resolution = normalizeResolution(rawResolution);
  const credentials = channelCredentials(ctx);
  let target = "none";
  body.resolution = resolution;
  if (credentials.mediaKey) {
    const policy = { "480p": ["480p", "720p"], "720p": ["480p", "1080p"], "1080p": ["720p", "1080p"] }[resolution];
    if (!policy) throw new Error("MediaKit resolution must be 480p, 720p, or 1080p");
    body.resolution = policy[0];
    target = policy[1];
    if (body.draft === true) throw new Error("MediaKit enhancement does not support draft output");
    // Ark callbacks describe only generation, not completion of the composite task.
    if (body.callback_url) throw new Error("MediaKit tasks must be polled; Ark callbacks do not describe final enhancement");
  }
  if (body.content !== undefined && !Array.isArray(body.content)) throw new Error("content must be an array");
  const content = Array.isArray(body.content) ? body.content.slice() : [];
  if (req.prompt !== undefined && trimmed(req.prompt)) {
    const texts = content.filter((item) => item && item.type === "text").map((item) => item.text).join("\n");
    if (texts !== req.prompt) content.push({ type: "text", text: String(req.prompt) });
  }
  if (req.images !== undefined && !Array.isArray(req.images)) throw new Error("images must be an array");
  const images = [req.image, req.input_reference].concat(req.images || []);
  for (const image of images) {
    if (!trimmed(image)) continue;
    if (typeof image !== "string") throw new Error("image references must be URLs");
    if (!content.some((item) => item && item.type === "image_url" && item.image_url && item.image_url.url === image)) content.push({ type: "image_url", image_url: { url: image } });
  }
  if (!content.length || content.length > MAX_COUNT) throw new Error("content must contain between 1 and 128 items");
  for (const item of content) {
    if (!item || typeof item !== "object" || Array.isArray(item)) throw new Error("content items must be objects");
    if (!["text", "image_url", "video_url", "audio_url", "draft_task"].includes(item.type)) throw new Error("Unsupported content type");
    if (item.type === "text" && !trimmed(item.text)) throw new Error("Text content must not be empty");
    if (["image_url", "video_url", "audio_url"].includes(item.type) && (!item[item.type] || typeof item[item.type].url !== "string" || !trimmed(item[item.type].url))) throw new Error("Media content requires a URL");
    if (item.type === "text" && /(?:^|\s)--(?:duration|dur|d|seconds|frames|f|fps|framespersecond|resolution|res|rs|ratio|rt|r|size)(?:\s|=|$)/i.test(item.text || "")) throw new Error("Use structured duration, frames, resolution and ratio fields instead of prompt flags");
  }
  body.content = rewriteDraftTaskContent(content, ctx.originTasks);
  body.model = ctx.upstreamModel || ctx.model || req.model;
  return { body: body, seconds: seconds, target: target, credentials: credentials };
}

function draftTaskIds(content) {
  const ids = [];
  if (!Array.isArray(content)) return ids;
  for (const item of content) {
    if (!item || typeof item !== "object" || Array.isArray(item)) continue;
    if (item.type !== "draft_task") continue;
    const draft = item.draft_task;
    if (!draft || typeof draft !== "object" || Array.isArray(draft)) continue;
    const id = trimmed(draft.id);
    if (id) ids.push(id);
  }
  return ids;
}

function rewriteDraftTaskContent(content, originTasks) {
  if (!Array.isArray(content)) return content;
  return content.map(function (item) {
    if (!item || typeof item !== "object" || Array.isArray(item) || item.type !== "draft_task") return item;
    const draft = item.draft_task;
    if (!draft || typeof draft !== "object" || Array.isArray(draft) || !trimmed(draft.id)) return item;
    const publicId = trimmed(draft.id);
    let upstream = "";
    if (Array.isArray(originTasks)) {
      for (const task of originTasks) {
        if (task && task.taskId === publicId) {
          upstream = trimmed(task.upstreamTaskId);
          break;
        }
      }
    }
    if (!upstream) throw new Error("origin task is unavailable");
    return Object.assign({}, item, { draft_task: Object.assign({}, draft, { id: upstream }) });
  });
}

function normalizeResolution(value) {
  const raw = trimmed(value).toLowerCase();
  if (["480p", "720p", "1080p", "4k"].includes(raw)) return raw;
  const parts = raw.replace("*", "x").split("x");
  if (parts.length !== 2 || parts.some((part) => !/^\d+$/.test(part) || Number(part) < 1 || Number(part) > 7680)) throw new Error("Invalid video resolution");
  const max = Math.max(Number(parts[0]), Number(parts[1]));
  if (max >= 3840) return "4k";
  if (max >= 1920) return "1080p";
  if (max >= 1280) return "720p";
  return "480p";
}

function hasVideo(content) {
  return Array.isArray(content) && content.some((item) => item && (item.type === "video_url" || Object.prototype.hasOwnProperty.call(item, "video_url")));
}

// Max-pixel 16:9 dimensions per resolution tier. Used when ratio is absent or
// adaptive so the submit-time estimate overestimates rather than underestimates.
// Official Ark formula: tokens = seconds × width × height × 24 / 1024.
// Reference clips are reserved conservatively; completion overlays the real bill.
function resolutionMaxPixels(resolution) {
  if (resolution === "480p") return [854, 480];
  if (resolution === "1080p") return [1920, 1080];
  if (resolution === "4k") return [3840, 2160];
  return [1280, 720];
}

function estimateTokens(seconds, resolution) {
  const dims = resolutionMaxPixels(resolution);
  return (seconds * dims[0] * dims[1] * 24) / 1024;
}

function videoInputRatio(model, resolution, content) {
  const video = hasVideo(content);
  const res = trimmed(resolution).toLowerCase();
  if (model === "doubao-seedance-2-5-260628") {
    if (res === "1080p") return video ? 7.0 / 10.7 : 11.7 / 10.7;
    return video ? 42 / 70 : 1;
  }
  if (model === "doubao-seedance-2-0-260128") {
    if (res === "1080p") return video ? 31 / 46 : 51 / 46;
    if (res === "4k") return video ? 16 / 46 : 26 / 46;
    return video ? 28 / 46 : 1;
  }
  if (model === "doubao-seedance-2-0-fast-260128") return video ? 22 / 37 : 1;
  if (model === "doubao-seedance-2-0-mini-260615") return video ? 14 / 23 : 1;
  return 1;
}

function responsesInput(req) {
  const texts = [],
    images = [];
  const input = req.input;
  if (typeof input === "string") texts.push(input);
  else if (Array.isArray(input)) {
    for (const item of input) {
      if (typeof item === "string") {
        texts.push(item);
        continue;
      }
      if (!item || typeof item !== "object" || Array.isArray(item)) continue;
      const content = item.content === undefined ? [item] : Array.isArray(item.content) ? item.content : [item.content];
      for (const part of content) {
        if (typeof part === "string") {
          texts.push(part);
          continue;
        }
        if (!part || typeof part !== "object" || Array.isArray(part)) continue;
        if (["input_text", "text"].includes(part.type) && typeof part.text === "string") texts.push(part.text);
        if (["input_image", "image_url"].includes(part.type)) {
          let image = part.image_url;
          if (image && typeof image === "object") image = image.url;
          if (trimmed(image)) images.push(trimmed(image));
        }
      }
    }
  }
  return {
    prompt: texts
      .filter(function (text) {
        return trimmed(text);
      })
      .join("\n"),
    images: images,
  };
}

function responsesVideoText(ctx) {
  const artifact = ctx && ctx.artifacts && ctx.artifacts.video;
  const url = trimmed(artifact && artifact.url);
  if (!url) throw new Error("video artifact is unavailable");
  const escaped = url.replace(/&/g, "&amp;").replace(/"/g, "&quot;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
  return '<video controls src="' + escaped + '"></video>';
}

export const native = {
  createTask: function (ctx) {
    if (!ctx.body || ctx.body.kind !== "json") throw new Error("JSON body required");
    const body = ctx.body.value;
    if (!body || typeof body !== "object" || Array.isArray(body)) throw new Error("request body must be an object");
    const model = trimmed(body.model);
    if (!model) throw new Error("model is required");
    if (body.content !== undefined && !Array.isArray(body.content)) throw new Error("content must be an array");
    const content = Array.isArray(body.content) ? body.content : [];
    const texts = [];
    let hasReference = false;
    for (const item of content) {
      if (!item || typeof item !== "object" || Array.isArray(item)) continue;
      if (item.type === "text" && typeof item.text === "string") texts.push(item.text);
      else hasReference = true;
    }
    if (!texts.length && !hasReference) throw new Error("content is required");
    const requestBody = {
      model: model,
      prompt: texts
        .filter(function (text) {
          return trimmed(text);
        })
        .join("\n"),
      metadata: body,
    };
    const seconds = Number(body.duration);
    if (Number.isFinite(seconds) && seconds > 0) requestBody.seconds = seconds;
    const intent = { kind: "submit", model: model, action: hasReference ? "image_to_video" : "text_to_video", requestBody: requestBody };
    const originTaskIds = draftTaskIds(content);
    if (originTaskIds.length) intent.originTaskIds = originTaskIds;
    return intent;
  },
  taskCreated: function (ctx, task) {
    const data = task.data && typeof task.data === "object" && !Array.isArray(task.data) ? task.data : {};
    return Object.assign({}, data, { id: task.task_id });
  },
  taskStatus: function (ctx, task) {
    const data = task.data || {};
    const statusMap = { NOT_START: "queued", SUBMITTED: "queued", QUEUED: "queued", IN_PROGRESS: "running", SUCCESS: "succeeded", FAILURE: "failed" };
    const output = { id: task.task_id, status: statusMap[task.status] || "unknown" };
    if (task.status === "SUCCESS") {
      if (data.result) {
        output.content = { video_url: mediaKitVideoURL(data) };
        if (data.result.resolution) output.resolution = data.result.resolution;
      } else {
        for (const key of ["content", "usage", "resolution", "duration", "ratio", "seed", "framespersecond"]) {
          if (data[key] !== undefined) output[key] = data[key];
        }
      }
    }
    if (task.fail_reason) output.error = { message: task.fail_reason };
    return output;
  },
  error: function (ctx, error) {
    return { error: { code: error.code, message: error.message } };
  },
};

export function buildSubmitRequest(ctx) {
  const generation = generationRequest(ctx);
  const body = generation.body;
  const hasReference = body.content.some((item) => item.type !== "text");
  return {
    url: ctx.baseUrl.replace(/\/+$/, "") + "/api/v3/contents/generations/tasks",
    method: "POST",
    headers: { "Content-Type": "application/json", Accept: "application/json", Authorization: "Bearer " + generation.credentials.arkKey },
    body: body,
    action: hasReference ? "image_to_video" : "text_to_video",
    rewriteModel: body.model,
  };
}

export function parseSubmitResponse(ctx, resp) {
  if (!resp.body || !trimmed(resp.body.id)) throw new Error("task_id is empty");
  const generation = generationRequest(ctx);
  const result = { taskId: resp.body.id, taskData: resp.body };
  if (generation.target !== "none") {
    result.state = {
      version: 1,
      phase: "generation",
      target: generation.target,
      resolution: generation.body.resolution,
      seconds: generation.seconds,
    };
  }
  return result;
}

export function extractUsage(ctx) {
  const generation = generationRequest(ctx);
  const body = generation.body;
  if (ctx.usagePurpose === "billing_ratios") {
    // Preserve ordinary legacy ratio billing. Composite pricing must be an
    // administrator-owned task expression; never hide enhancement prices here.
    const ratio = videoInputRatio(ctx.upstreamModel || ctx.model, body.resolution, body.content);
    return ratio === 1 ? null : { video_input_ratio: ratio };
  }
  // A reference clip can contribute billing tokens too. Reserve 15 seconds per
  // reference (Ark's clip limit); measured completion tokens replace the estimate.
  const referenceSeconds = body.content.filter((item) => item.type === "video_url").length * 15;
  return {
    tokens: Math.ceil(estimateTokens(generation.seconds + referenceSeconds, body.resolution)),
    resolution: body.resolution,
    video_input: hasVideo(body.content) ? "video" : "none",
    enhancement_seconds: generation.target === "none" ? 0 : generation.seconds,
    enhancement_resolution: generation.target,
  };
}

export function buildQueryRequest(ctx) {
  const credentials = channelCredentials(ctx);
  const state = ctx.state;
  if (state && state.version === 1 && state.phase !== "generation") {
    if (!credentials.mediaKey) throw new Error("MediaKit credentials are required to continue this task");
    const headers = { Accept: "application/json", "Content-Type": "application/json", Authorization: "Bearer " + credentials.mediaKey };
    if (state.phase === "enhancement_submit") {
      return {
        url: credentials.mediaBaseUrl + "/api/v1/tools/enhance-video",
        method: "POST",
        headers: headers,
        body: {
          video_url: state.videoUrl,
          scene: "aigc",
          tool_version: "standard",
          resolution: state.target,
          // Independent of credentials, clock and process. A lost response or
          // crash before state persistence replays exactly the same vendor key.
          client_token: "new-api-" + utils.hmacSHA256(ctx.taskId + "\u0000" + state.target, "doubao-mediakit-v1").slice(0, 32),
        },
      };
    }
    if (state.phase !== "enhancement") throw new Error("Unknown MediaKit task phase");
    return { url: credentials.mediaBaseUrl + "/api/v1/tasks/" + encodeURIComponent(state.taskId), method: "GET", headers: headers };
  }
  if (state && state.version !== 1) throw new Error("Unsupported Doubao task state version");
  return {
    url: ctx.baseUrl.replace(/\/+$/, "") + "/api/v3/contents/generations/tasks/" + encodeURIComponent(ctx.taskId),
    method: "GET",
    headers: { Accept: "application/json", "Content-Type": "application/json", Authorization: "Bearer " + credentials.arkKey },
  };
}

export function parseTaskResult(ctx, body, response) {
  if (!body || typeof body !== "object") return { status: "UNKNOWN", reason: "Invalid task response" };
  const state = ctx.state;
  if (state && state.version === 1 && state.phase !== "generation") {
    if (body.success === false || ["failed", "canceled", "cancelled", "expired"].includes(body.status)) {
      return { status: "FAILURE", progress: "100%", reason: "MediaKit enhancement failed" };
    }
    if (state.phase === "enhancement_submit") {
      if ((response && response.status >= 400) || !trimmed(body.task_id)) return { status: "UNKNOWN", reason: "MediaKit did not acknowledge enhancement submission" };
      return { status: "IN_PROGRESS", progress: "75%", state: Object.assign({}, state, { phase: "enhancement", taskId: body.task_id }) };
    }
    if (["completed", "succeeded", "success"].includes(body.status)) {
      const url = mediaKitVideoURL(body);
      if (!url) return { status: "FAILURE", progress: "100%", reason: "MediaKit completed without a video URL" };
      return { status: "SUCCESS", progress: "100%", url: url, completionTokens: state.tokens, totalTokens: state.tokens };
    }
    if (["queued", "running", "processing"].includes(body.status)) return { status: "IN_PROGRESS", progress: "85%" };
    return { status: "UNKNOWN", reason: "Unrecognized MediaKit task status" };
  }
  if (body.status === "pending" || body.status === "queued") return { status: "QUEUED", progress: "10%" };
  if (body.status === "processing" || body.status === "running") return { status: "IN_PROGRESS", progress: "50%" };
  if (body.status === "succeeded") {
    if (state && state.phase === "generation") {
      const url = trimmed(body.content && body.content.video_url);
      if (!url) return { status: "FAILURE", progress: "100%", reason: "Ark completed without a video URL" };
      const facts = arkCompletionFacts(body);
      const next = Object.assign({}, state, { phase: "enhancement_submit", videoUrl: url });
      if (facts.tokens !== undefined) next.tokens = facts.tokens;
      // The enhancement duration is the actual source clip duration, not an
      // upstream deduction number and never the sum of input + output durations.
      const seconds = Number(body.duration);
      if (Number.isFinite(seconds) && seconds > 0 && seconds <= MAX_SECONDS) next.seconds = seconds;
      if (facts.resolution) next.resolution = facts.resolution;
      return { status: "IN_PROGRESS", progress: "65%", state: next };
    }
    const result = { status: "SUCCESS", progress: "100%", url: body.content && body.content.video_url ? body.content.video_url : "" };
    const usage = body.usage || {};
    const completionTokens = Number(usage.completion_tokens || 0);
    const totalTokens = Number(usage.total_tokens || 0);
    if (Number.isFinite(completionTokens) && completionTokens > 0) result.completionTokens = completionTokens;
    if (Number.isFinite(totalTokens) && totalTokens > 0) result.totalTokens = totalTokens;
    return result;
  }
  if (body.status === "failed" || body.status === "expired" || body.status === "cancelled") {
    const reason = body.error && body.error.message ? body.error.message : body.status;
    return { status: "FAILURE", progress: "100%", reason: reason };
  }
  return { status: "UNKNOWN", reason: "unrecognized status: " + String(body.status || "") };
}

function artifactData(ctx) {
  const data = (ctx && ctx.data) || {};
  if (data.data && typeof data.data === "object" && data.data.task_id && Object.prototype.hasOwnProperty.call(data.data, "data")) return data.data.data || {};
  return data;
}

export function listArtifacts(task) {
  if (task.status !== "SUCCESS") return [];
  const data = artifactData(task);
  const content = data.content || {};
  if (data.result) return mediaKitVideoURL(data) ? [{ key: "video", type: "video", mimeType: "video/mp4" }] : [];
  const artifacts = [];
  if (trimmed(content.video_url)) artifacts.push({ key: "video", type: "video" });
  if (trimmed(content.last_frame_url)) artifacts.push({ key: "last_frame", type: "image", mimeType: "image/png" });
  return artifacts;
}

export function buildContentRequest(ctx) {
  const data = artifactData(ctx);
  const content = data.content || {};
  const urls = { video: data.result ? mediaKitVideoURL(data) : content.video_url, last_frame: content.last_frame_url };
  const url = trimmed(urls[ctx.artifactKey]);
  if (!url) throw new Error("artifact_not_found");
  return { url: url, method: ctx.clientRequest.method, credentialless: true };
}

function arkCompletionFacts(body) {
  const facts = {};
  const usage = body.usage || {};
  const rawTokens = usage.completion_tokens ?? usage.total_tokens;
  if (typeof rawTokens === "number" && Number.isFinite(rawTokens) && rawTokens >= 0) facts.tokens = rawTokens;
  const content = body.content || {};
  const resolution = trimmed(content.resolution || body.resolution).toLowerCase();
  if (["480p", "720p", "1080p", "4k"].includes(resolution)) facts.resolution = resolution;
  return facts;
}

export function extractUsageOnComplete(task, taskResult, body) {
  const state = task.state;
  if (state && state.version === 1) {
    if (state.phase !== "enhancement" || !body || !["completed", "succeeded", "success"].includes(body.status)) return {};
    const facts = { resolution: state.resolution, enhancement_seconds: state.seconds, enhancement_resolution: state.target };
    if (state.tokens !== undefined) facts.tokens = state.tokens;
    return facts;
  }
  if (!body || body.status !== "succeeded") return {};
  return arkCompletionFacts(body);
}

// MediaKit response formats use an explicit result envelope; do not recursively
// choose an arbitrary URL (it might be a thumbnail or the unenhanced source).
function mediaKitVideoURL(body) {
  const result = body.result || {};
  const video = result.video || {};
  return trimmed(result.video_url || result.output_url || result.url || video.video_url || video.url);
}

export const protocols = {
  openai_responses: {
    decodeRequest: function (ctx) {
      if (!ctx.body || ctx.body.kind !== "json") throw new Error("JSON body required");
      const req = ctx.body.value;
      if (!req || typeof req !== "object" || Array.isArray(req)) throw new Error("request body must be an object");
      const model = trimmed(req.model);
      if (!model) throw new Error("model is required");
      if (req.input !== undefined && typeof req.input !== "string" && !Array.isArray(req.input)) throw new Error("input must be a string or array");
      if (req.images !== undefined && !Array.isArray(req.images)) throw new Error("images must be an array");
      if (req.metadata !== undefined && (!req.metadata || typeof req.metadata !== "object" || Array.isArray(req.metadata)))
        throw new Error("metadata must be an object");
      const input = responsesInput(req);
      const prompt = input.prompt || trimmed(req.prompt);
      const images = [];
      for (const image of [req.image, req.input_reference].concat(req.images || [], input.images)) {
        if (trimmed(image) && !images.includes(trimmed(image))) images.push(trimmed(image));
      }
      if (!prompt && images.length === 0 && !(Array.isArray((req.metadata || {}).content) && req.metadata.content.length) && !(Array.isArray(req.content) && req.content.length)) throw new Error("input is required");
      const metadata = Object.assign({}, req.metadata || {});
      if (Object.prototype.hasOwnProperty.call(req, "resolution")) metadata.resolution = req.resolution;
      else if (req.size && !metadata.resolution) metadata.resolution = normalizeResolution(req.size);
      const requestBody = Object.assign({}, req, { model: model, prompt: prompt, metadata: metadata });
      if (images.length) requestBody.images = images;
      if (Object.prototype.hasOwnProperty.call(req, "seconds")) requestBody.seconds = req.seconds;
      else if (Object.prototype.hasOwnProperty.call(req, "duration")) requestBody.seconds = req.duration;
      if (Object.prototype.hasOwnProperty.call(req, "size")) requestBody.size = req.size;
      const intent = { kind: "submit", model: model, action: images.length ? "image_to_video" : "text_to_video", requestBody: requestBody };
      const originTaskIds = draftTaskIds(req.content || metadata.content);
      if (originTaskIds.length) intent.originTaskIds = originTaskIds;
      return intent;
    },
    renderEvents: function (ctx, task, previousState) {
      const status = String(task.status || "UNKNOWN").toUpperCase();
      const value = Number(String(task.progress || "").replace("%", ""));
      const progress = Number.isFinite(value) && value >= 0 && value <= 100 ? value : null;
      const state = { status: status, progress: progress };
      if (status === "SUCCESS") {
        const text = responsesVideoText(ctx);
        const events = previousState && previousState.status === status ? [] : [{ type: "output", data: text }];
        return { events: events, state: state, done: true };
      }
      if (status === "FAILURE")
        return { events: [{ type: "error", code: "task_failed", message: task.fail_reason || "task failed" }], state: state, done: true };
      if (previousState && previousState.status === status && previousState.progress === progress) return { events: [], state: state, done: false };
      const event = { type: "progress", message: status.toLowerCase() };
      if (progress !== null) event.progress = progress;
      return { events: [event], state: state, done: false };
    },
    renderFinal: function (ctx, _task) {
      return {
        output: [
          {
            type: "message",
            status: "completed",
            role: "assistant",
            content: [{ type: "output_text", text: responsesVideoText(ctx), annotations: [], logprobs: [] }],
          },
        ],
        metadata: { vendor: "doubao" },
      };
    },
  },
};

const legacyRenderers = {
  openai_video: function (task) {
    const data = task.data || {};
    const statusMap = { NOT_START: "queued", SUBMITTED: "queued", QUEUED: "queued", IN_PROGRESS: "in_progress", SUCCESS: "completed", FAILURE: "failed" };
    const output = {
      id: task.task_id,
      object: "video",
      model: task.properties ? task.properties.origin_model_name || "" : "",
      status: statusMap[task.status] || "unknown",
      progress: Number(String(task.progress || "0").replace("%", "")),
      created_at: task.created_at,
      completed_at: task.updated_at,
    };
    if (data.status === "failed") output.error = { message: data.error ? data.error.message || "" : "", code: data.error ? data.error.code || "" : "" };
    return output;
  },
};

function videoSubmitIntent(model, req) {
  const seconds = req.seconds ?? req.duration;
  if (seconds !== undefined) boundedInteger(seconds, "seconds", MAX_SECONDS);
  const intent = {
    kind: "submit",
    model: model,
    action: req.input_reference || req.image ? "image_to_video" : "text_to_video",
    requestBody: Object.assign({}, req, { model: model }),
  };
  const originTaskIds = draftTaskIds(req.content || (req.metadata || {}).content);
  if (originTaskIds.length) intent.originTaskIds = originTaskIds;
  return intent;
}

protocols.openai_video = {
  decodeRequest: function (ctx) {
    if (!ctx.body || (ctx.body.kind !== "json" && ctx.body.kind !== "multipart")) throw new Error("JSON or multipart body required");
    if (ctx.body.kind === "json") {
      if (!ctx.body.value || Array.isArray(ctx.body.value)) throw new Error("JSON object required");
      const req = ctx.body.value;
      return videoSubmitIntent(ctx.model, req);
    }
    const first = function (name) {
      const values = (ctx.body.fields || {})[name] || [];
      if (values.length > 1) throw new Error(name + " must be provided once");
      return values[0];
    };
    const req = {};
    const fields = ctx.body.fields || {};
    for (const name of Object.keys(fields)) {
      req[name] = first(name);
    }
    if (req.metadata !== undefined) {
      let parsed;
      try {
        parsed = JSON.parse(req.metadata);
      } catch (e) {
        throw new Error("metadata must be a JSON object string");
      }
      if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) throw new Error("metadata must be a JSON object string");
      req.metadata = parsed;
    }
    if ((ctx.body.files || []).length) throw new Error("Doubao requires image and video references to be URLs inside metadata.content");
    if (req.seconds !== undefined) req.seconds = Number(req.seconds);
    else if (req.duration !== undefined) req.seconds = Number(req.duration);
    return videoSubmitIntent(ctx.model, req);
  },
  render: function (ctx, task) {
    return legacyRenderers.openai_video(task);
  },
};
