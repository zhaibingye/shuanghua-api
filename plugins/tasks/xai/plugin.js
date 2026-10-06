// new-api fork: preserve xAI's native and OpenAI-compatible video contracts.
export const meta = {
  apiVersion: 1,
  key: "xai",
  name: "xAI Video",
  version: "1.0.0",
  author: { name: "QuantumNous" },
  icon: "Grok",
  channelTypes: [48],
  models: ["grok-imagine-video", "grok-imagine-video-1.5", "grok-imagine-video-1.5-preview"],
  fetchMode: "per_task",
  baseUrl: "https://api.x.ai",
  usageSchema: { seconds: { type: "number", unit: "second" } },
  routes: [
    { method: "POST", path: "/xai/v1/videos", type: "submit", decode: "create", render: "render" },
    { method: "POST", path: "/xai/v1/videos/generations", type: "submit", decode: "create", render: "render" },
    { method: "POST", path: "/xai/v1/videos/edits", type: "submit", decode: "create", render: "render" },
    { method: "POST", path: "/xai/v1/videos/extensions", type: "submit", decode: "create", render: "render" },
    { method: "GET", path: "/xai/v1/videos/:task_id", type: "query", render: "render" },
  ],
  protocols: [{ name: "openai_video" }],
};

function seconds(req) {
  const raw = req._nativePath ? (req.duration ?? 4) : (req.seconds ?? 4);
  if (typeof raw !== "number" && typeof raw !== "string") throw new Error("duration must be an integer");
  const value = Number(raw);
  if (!Number.isInteger(value) || value < 1 || value > 15) throw new Error("duration must be an integer between 1 and 15");
  return value;
}

function action(req) {
  return req.image || req.input_reference || req.reference_images ? "image_to_video" : "text_to_video";
}

export function buildSubmitRequest(ctx) {
  const request = ctx.requestBody || {};
  seconds(request);
  const body = Object.assign({}, request, { model: ctx.upstreamModel || ctx.model });
  const path = request._nativePath || "/openai/v1/videos";
  delete body._nativePath;
  if (!["/v1/videos", "/v1/videos/generations", "/v1/videos/edits", "/v1/videos/extensions", "/openai/v1/videos"].includes(path))
    throw new Error("unsupported xAI video endpoint");
  const headers = { Authorization: "Bearer " + ctx.apiKey, "Content-Type": "application/json" };
  if ((ctx.files || []).length || (ctx.requestHeaders || {})["Content-Type"]?.includes("multipart/form-data")) {
    const parts = Object.keys(body).map((key) => ({ name: key, value: body[key] }));
    for (const file of ctx.files || []) parts.push({ name: file.field, fileRef: file.ref });
    delete headers["Content-Type"];
    return { url: ctx.baseUrl + path, method: "POST", headers: headers, bodyType: "multipart", parts: parts };
  }
  return { url: ctx.baseUrl + path, method: "POST", headers: headers, body: body };
}

export function parseSubmitResponse(ctx, response) {
  const body = response.body || {};
  const id = body.request_id || body.id;
  if (!id) throw new Error("xAI response has no task ID");
  return { taskId: id, taskData: body };
}
export function extractUsage(ctx) {
  return { seconds: seconds(ctx.requestBody || {}) };
}
export function buildQueryRequest(ctx) {
  return { url: ctx.baseUrl + "/v1/videos/" + encodeURIComponent(ctx.taskId), method: "GET", headers: { Authorization: "Bearer " + ctx.apiKey } };
}
export function parseTaskResult(ctx, body) {
  const statuses = {
    queued: "QUEUED",
    pending: "QUEUED",
    in_progress: "IN_PROGRESS",
    processing: "IN_PROGRESS",
    running: "IN_PROGRESS",
    completed: "SUCCESS",
    done: "SUCCESS",
    succeeded: "SUCCESS",
    success: "SUCCESS",
    failed: "FAILURE",
    error: "FAILURE",
    expired: "FAILURE",
    cancelled: "FAILURE",
    canceled: "FAILURE",
  };
  return {
    status:
      statuses[
        String(body.status || "")
          .trim()
          .toLowerCase()
      ] || "UNKNOWN",
    progress: String(body.progress || 0) + "%",
    url: (body.video || {}).url || "",
    reason: typeof body.error === "string" ? body.error : (body.error || {}).message || "",
  };
}
export function listArtifacts(task) {
  return task.status === "SUCCESS" ? [{ key: "video", type: "video", mimeType: "video/mp4" }] : [];
}
export function buildContentRequest(ctx) {
  const url = ((ctx.data || {}).video || {}).url;
  if (ctx.artifactKey !== "video" || !url) throw new Error("artifact_not_found");
  return { url: url, method: ctx.clientRequest.method, credentialless: true };
}

export const native = {
  create: function (ctx) {
    const body = ctx.body && ctx.body.value;
    if (!body || !body.model) throw new Error("model is required");
    seconds(Object.assign({}, body, { _nativePath: ctx.path }));
    return { kind: "submit", model: body.model, action: action(body), requestBody: Object.assign({}, body, { _nativePath: ctx.path.replace(/^\/xai/, "") }) };
  },
  render: function (ctx, task) {
    const output = Object.assign({}, task.data || {}, { request_id: task.task_id });
    delete output.id;
    delete output.object;
    return output;
  },
};

export const protocols = {
  openai_video: {
    decodeRequest: function (ctx) {
      if (ctx.path && ctx.path.startsWith("/v1/videos")) return native.create(ctx);
      let body;
      if (ctx.body.kind === "json") body = Object.assign({}, ctx.body.value);
      else if (ctx.body.kind === "multipart" || ctx.body.kind === "form") {
        body = {};
        for (const key of Object.keys(ctx.body.fields || {})) {
          const values = ctx.body.fields[key];
          if (values.length !== 1) throw new Error("duplicate field");
          body[key] = values[0];
        }
      } else throw new Error("JSON or multipart body required");
      if (!body.prompt) throw new Error("prompt is required");
      delete body._nativePath;
      seconds(body);
      body.model = ctx.model;
      return { kind: "submit", model: ctx.model, action: action(body), requestBody: body };
    },
    render: function (ctx, task) {
      if (ctx.path && ctx.path.startsWith("/v1/videos")) return native.render(ctx, task);
      const source = task.data || {};
      const output = {
        id: task.task_id,
        object: "video",
        model: source.model || ctx.model,
        status: { SUCCESS: "completed", FAILURE: "failed", IN_PROGRESS: "in_progress" }[task.status] || "queued",
        progress: Number(String(task.progress || "0").replace("%", "")),
        created_at: task.created_at,
      };
      for (const key of ["prompt", "seconds", "size", "remixed_from_video_id", "expires_at"]) if (source[key] !== undefined) output[key] = source[key];
      if (task.status === "SUCCESS") {
        output.completed_at = task.finished_at;
        output.video_url = (source.video || {}).url;
      }
      if (task.status === "FAILURE") output.error = { code: "video_generation_failed", message: task.fail_reason };
      return output;
    },
  },
};
