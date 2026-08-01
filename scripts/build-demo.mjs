import { copyFile, mkdir, readFile, rm, writeFile } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const sourceRoot = path.join(repoRoot, "internal", "web", "static");
const outputRoot = path.resolve(process.argv[2] || path.join(repoRoot, "dist", "pages"));

await rm(outputRoot, { recursive: true, force: true });
await mkdir(outputRoot, { recursive: true });

for (const file of ["styles.css", "i18n.js", "app.js"]) {
  await copyFile(path.join(sourceRoot, file), path.join(outputRoot, file));
}
await copyFile(path.join(repoRoot, "scripts", "demo-api.js"), path.join(outputRoot, "demo-api.js"));

let html = await readFile(path.join(sourceRoot, "index.html"), "utf8");
html = html
  .replace('href="/styles.css"', 'href="./styles.css"')
  .replace('src="/i18n.js"', 'src="./i18n.js"')
  .replace('src="/app.js"', 'src="./app.js"')
  .replace('  <script src="./i18n.js"></script>', '  <script src="./demo-api.js"></script>\n  <script src="./i18n.js"></script>');
await writeFile(path.join(outputRoot, "index.html"), html);
await writeFile(path.join(outputRoot, ".nojekyll"), "");

console.log(`Built synthetic GitHub Pages demo at ${outputRoot}`);
