import http from "node:http";
import { readFile, stat } from "node:fs/promises";
import path from "node:path";

const root = path.resolve(process.argv[2] || "dist/pages");
const port = Number(process.argv[3] || 43219);
const mount = `/${String(process.argv[4] || "").replace(/^\/+|\/+$/g, "")}`.replace(/\/$/, "");

const types = {
  ".css": "text/css; charset=utf-8",
  ".html": "text/html; charset=utf-8",
  ".js": "text/javascript; charset=utf-8",
  ".json": "application/json; charset=utf-8",
  ".png": "image/png",
  ".gif": "image/gif",
  ".mp4": "video/mp4"
};

http.createServer(async (request, response) => {
  const url = new URL(request.url, `http://127.0.0.1:${port}`);
  let pathname = decodeURIComponent(url.pathname);
  if (mount && pathname.startsWith(`${mount}/`)) pathname = pathname.slice(mount.length);
  else if (mount && pathname === mount) pathname = "/";
  let relative = pathname === "/" ? "index.html" : pathname.replace(/^\/+/, "");
  let file = path.resolve(root, relative);
  if (!file.startsWith(`${root}${path.sep}`) && file !== path.join(root, "index.html")) {
    response.writeHead(403);
    response.end("Forbidden");
    return;
  }
  try {
    if ((await stat(file)).isDirectory()) file = path.join(file, "index.html");
    const content = await readFile(file);
    response.writeHead(200, { "Content-Type": types[path.extname(file)] || "application/octet-stream", "Cache-Control": "no-store" });
    response.end(content);
  } catch {
    try {
      const content = await readFile(path.join(root, "index.html"));
      response.writeHead(200, { "Content-Type": types[".html"], "Cache-Control": "no-store" });
      response.end(content);
    } catch {
      response.writeHead(404);
      response.end("Not Found");
    }
  }
}).listen(port, "127.0.0.1", () => {
  console.log(`Static server: http://127.0.0.1:${port}${mount || "/"}/`);
});
