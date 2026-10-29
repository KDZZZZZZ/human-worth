import { api, enc } from "./api.js";
export const uploadTypes = [
  "image/png",
  "image/jpeg",
  "image/webp",
  "application/pdf",
  "text/plain",
];
export async function uploadFile(file, session, onSession, onProgress) {
  if (!file.size || file.size > 33554432)
    throw Error("Files must be between 1 byte and 32 MiB.");
  const mediaType = file.type || "text/plain";
  if (!uploadTypes.includes(mediaType))
    throw Error("Supported files: PNG, JPEG, WebP, PDF and plain text.");
  if (!session) {
    const sha256 = Array.from(
      new Uint8Array(
        await crypto.subtle.digest("SHA-256", await file.arrayBuffer()),
      ),
    )
      .map((b) => b.toString(16).padStart(2, "0"))
      .join("");
    const created = await api("/me/assets/upload-sessions", {
      method: "POST",
      body: {
        filename: file.name,
        mediaType,
        expectedBytes: file.size,
        sha256,
      },
    });
    session = {
      file,
      assetId: created.asset.id,
      sessionId: created.uploadSession.id,
    };
    onSession(session);
  }
  const current = await api("/me/upload-sessions/" + enc(session.sessionId));
  let offset = current.receivedBytes;
  while (offset < file.size) {
    const end = Math.min(offset + 5242880, file.size);
    onProgress(`Uploading ${Math.round((offset / file.size) * 100)}%`);
    const result = await api(
      "/me/assets/upload-sessions/" + enc(session.sessionId) + "/content",
      {
        method: "PUT",
        body: file.slice(offset, end),
        headers: {
          "Content-Type": "application/octet-stream",
          "Content-Range": `bytes ${offset}-${end - 1}/${file.size}`,
        },
      },
    );
    if (result.nextOffset <= offset || result.nextOffset > file.size)
      throw Error("Upload progress is unavailable. Check progress and try again.");
    offset = result.nextOffset;
  }
  const asset = await api("/me/assets/" + enc(session.assetId) + "/complete", {
    method: "POST",
    body: { uploadSessionId: session.sessionId },
  });
  if (asset.state !== "ready") throw Error("File verification is not complete.");
  return asset;
}
