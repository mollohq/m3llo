// Does a text on screen have real glyphs? The accessibility tree gives the
// string, not the pixels: a name with no font shows as nothing, or as equal
// boxes (tofu), and still reads right in the tree. This reads the pixels of
// the screenshot under the text element.
//
// The check, for a text of two or more different characters:
// - the element has ink (pixels that are not the background), and
// - the first and the last character cell of the ink differ. Two tofu boxes
//   are the same picture, so they do not differ.

import { inflateSync } from "node:zlib";

import type { App } from "../../../tools/mello-driver/src/app.ts";

export type Bitmap = { width: number; height: number; channels: number; data: Uint8Array };

/** Decode a PNG of 8 bits per channel, RGB or RGBA, not interlaced (what Slint writes). */
export function decodePng(png: Uint8Array): Bitmap {
  const view = new DataView(png.buffer, png.byteOffset, png.byteLength);
  let pos = 8;
  let width = 0;
  let height = 0;
  let channels = 0;
  const idat: Uint8Array[] = [];
  while (pos < png.length) {
    const len = view.getUint32(pos);
    const type = String.fromCharCode(...png.subarray(pos + 4, pos + 8));
    const body = png.subarray(pos + 8, pos + 8 + len);
    if (type === "IHDR") {
      width = view.getUint32(pos + 8);
      height = view.getUint32(pos + 12);
      const depth = body[8];
      const colour = body[9];
      if (depth !== 8 || (colour !== 2 && colour !== 6) || body[12] !== 0) {
        throw new Error(`unsupported PNG: depth ${depth}, colour type ${colour}, interlace ${body[12]}`);
      }
      channels = colour === 6 ? 4 : 3;
    } else if (type === "IDAT") idat.push(body);
    pos += 12 + len;
  }
  const raw = inflateSync(Buffer.concat(idat));
  const stride = width * channels;
  const data = new Uint8Array(stride * height);
  for (let y = 0; y < height; y++) {
    const filter = raw[y * (stride + 1)];
    const line = raw.subarray(y * (stride + 1) + 1, (y + 1) * (stride + 1));
    const out = y * stride;
    for (let i = 0; i < stride; i++) {
      const a = i >= channels ? data[out + i - channels] : 0;
      const b = y > 0 ? data[out - stride + i] : 0;
      const c = i >= channels && y > 0 ? data[out - stride + i - channels] : 0;
      let v = line[i];
      if (filter === 1) v += a;
      else if (filter === 2) v += b;
      else if (filter === 3) v += (a + b) >> 1;
      else if (filter === 4) {
        const p = a + b - c;
        const pa = Math.abs(p - a);
        const pb = Math.abs(p - b);
        const pc = Math.abs(p - c);
        v += pa <= pb && pa <= pc ? a : pb <= pc ? b : c;
      }
      data[out + i] = v & 0xff;
    }
  }
  return { width, height, channels, data };
}

export type Ink = {
  /** Ink pixels in the element. */
  ink: number;
  /** The share of pixels that differ between the first and the last character cell, 0 to 1. */
  differ: number;
};

/** The ink of a rectangle of the bitmap (device pixels), split in `cells` equal cells. */
export function inkOf(bmp: Bitmap, x0: number, y0: number, w: number, h: number, cells: number): Ink {
  const lum = (x: number, y: number) => {
    const i = (y * bmp.width + x) * bmp.channels;
    return (bmp.data[i] * 299 + bmp.data[i + 1] * 587 + bmp.data[i + 2] * 114) / 1000;
  };
  // The background is the most common luminance. Ink is far from it.
  const hist = new Array<number>(256).fill(0);
  for (let y = y0; y < y0 + h; y++) for (let x = x0; x < x0 + w; x++) hist[Math.round(lum(x, y))]++;
  const bg = hist.indexOf(Math.max(...hist));
  const isInk = (x: number, y: number) => Math.abs(lum(x, y) - bg) > 60;
  let left = -1;
  let right = -1;
  let ink = 0;
  for (let x = x0; x < x0 + w; x++) {
    for (let y = y0; y < y0 + h; y++) {
      if (!isInk(x, y)) continue;
      ink++;
      if (left < 0) left = x;
      right = x;
    }
  }
  if (ink === 0) return { ink: 0, differ: 0 };
  const cell = Math.floor((right - left + 1) / cells);
  if (cell < 2) return { ink, differ: 0 };
  const lastLeft = right + 1 - cell;
  let diff = 0;
  let either = 0;
  for (let dx = 0; dx < cell; dx++) {
    for (let y = y0; y < y0 + h; y++) {
      const a = isInk(left + dx, y);
      const b = isInk(lastLeft + dx, y);
      if (a || b) either++;
      if (a !== b) diff++;
    }
  }
  return { ink, differ: either === 0 ? 0 : diff / either };
}

/**
 * The ink of the text element on screen whose text is exactly `text`. When
 * more than one element has the text (one of them under the wash of a modal),
 * the one with the most ink counts. It reads the screen until the text has
 * ink, for at most `timeoutMs`: a list that loads again gives a frame with no
 * element, and a text can come a frame after its row. A text with no glyph
 * has no ink at the end of the wait either. Throws when no such element
 * comes on screen.
 */
export async function textInk(app: App, text: string, timeoutMs = 5_000): Promise<Ink> {
  const deadline = Date.now() + timeoutMs;
  let best: Ink | null = null;
  for (;;) {
    const els = (await app.ui.textElements()).filter((t) => t.text === text && t.width > 0 && t.height > 0);
    if (els.length > 0) {
      const bmp = decodePng(await app.ui.screenshot());
      const [win] = await app.ui.props([await app.ui.window()]);
      const scale = win?.size ? bmp.width / win.size.width : 1;
      best = { ink: 0, differ: 0 };
      for (const el of els) {
        const x0 = Math.max(0, Math.floor(el.x * scale));
        const y0 = Math.max(0, Math.floor(el.y * scale));
        const w = Math.min(bmp.width - x0, Math.ceil(el.width * scale));
        const h = Math.min(bmp.height - y0, Math.ceil(el.height * scale));
        if (w <= 0 || h <= 0) continue;
        const ink = inkOf(bmp, x0, y0, w, h, Array.from(text).length);
        if (ink.ink > best.ink) best = ink;
      }
      if (best.ink > 0) return best;
    }
    if (Date.now() > deadline) break;
  }
  if (!best) throw new Error(`${app.name}: no text element "${text}" on screen`);
  return best;
}

/** A text of different characters shows different glyphs: ink, and cells that differ by more than a fifth. */
export function hasGlyphs(ink: Ink): boolean {
  return ink.ink > 0 && ink.differ > 0.2;
}
