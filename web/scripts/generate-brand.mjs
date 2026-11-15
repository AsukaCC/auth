// Rasterize the shared SVG artwork; npm run icons regenerates checked-in assets.
import { Resvg } from "@resvg/resvg-js";
import { readFile, writeFile } from "node:fs/promises";

const assets = new URL("../public/", import.meta.url);
const brand = await readFile(new URL("brand-mark.svg", assets), "utf8");
const png = (svg, width) => new Resvg(svg, { fitTo: { mode: "width", value: width }, font: { loadSystemFonts: true } }).render().asPng();
for (const [name, size] of [["favicon-16x16.png", 16], ["favicon-32x32.png", 32], ["apple-touch-icon.png", 180], ["icon-192.png", 192], ["icon-512.png", 512]]) {
  await writeFile(new URL(name, assets), png(brand, size));
}
await writeFile(new URL("icon-maskable-512.png", assets), png(await readFile(new URL("icon-maskable.svg", assets), "utf8"), 512));
await writeFile(new URL("social-preview.png", assets), png(await readFile(new URL("social-preview.svg", assets), "utf8"), 1200));
// ICO directory entries contain PNG payloads, supported by current browsers and Windows.
const sizes = [16, 32, 48];
const frames = sizes.map(size => png(brand, size));
const directory = Buffer.alloc(6 + sizes.length * 16);
directory.writeUInt16LE(1, 2);
directory.writeUInt16LE(sizes.length, 4);
let offset = directory.length;
frames.forEach((frame, index) => {
  const pos = 6 + index * 16;
  directory[pos] = directory[pos + 1] = sizes[index];
  directory.writeUInt16LE(1, pos + 4);
  directory.writeUInt16LE(32, pos + 6);
  directory.writeUInt32LE(frame.length, pos + 8);
  directory.writeUInt32LE(offset, pos + 12);
  offset += frame.length;
});
await writeFile(new URL("favicon.ico", assets), Buffer.concat([directory, ...frames]));
console.log("Generated favicon, Apple/Android icons and social preview.");
