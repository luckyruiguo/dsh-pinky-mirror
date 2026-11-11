#!/usr/bin/env node
/**
 * Pinky Mirror Bridge
 * 把 Pinky 桌面窗口里的手机镜像区截出来，以 MJPEG 流供 DSH 右侧边栏显示。
 *
 * 只读：不修改 Pinky 的窗口、样式、设置。
 *
 * 取图要点：
 *  - 用「区域抓取」而非 -window_id（后者在本机 X11 上偶发
 *    "Cannot get the image data event_error"，不稳定）
 *  - 抓取前确保 Pinky 窗口在前面（X11 是所见即所得）
 *  - 手机屏幕矩形自动检测，失败则回退到上次成功的矩形 / 环境变量
 */

import http from 'node:http';
import { spawn, execFile } from 'node:child_process';
import { promisify } from 'node:util';

const exec = promisify(execFile);

const PORT = Number(process.env.PINKY_BRIDGE_PORT || 7801);
const HOST = process.env.PINKY_BRIDGE_HOST || '127.0.0.1';
const DISPLAY = process.env.PINKY_BRIDGE_DISPLAY || process.env.DISPLAY || ':1';
const FPS = Number(process.env.PINKY_BRIDGE_FPS || 10);
const TARGET_W = Number(process.env.PINKY_BRIDGE_WIDTH || 640);
const KEEP_FRONT = process.env.PINKY_BRIDGE_KEEP_FRONT !== '0';
const MANUAL = process.env.PINKY_CROP || ''; // "x,y,w,h"（窗口内相对）

let frame = Buffer.alloc(0);
let seq = 0;
let geom = null;
let grab = null;
let winId = process.env.PINKY_WIN_ID || '';
let lastGood = null; // 上次成功的 { relX,relY,w,h }（窗口内相对）
const viewers = new Set();
const stats = { startedAt: Date.now(), frames: 0, lastFrameAt: 0, errors: 0, lastError: '', restarts: 0, detectFails: 0 };

// ---------------- 窗口 ----------------

async function findWin() {
  try {
    const { stdout } = await exec('xwininfo', ['-root', '-tree'], { timeout: 4000 });
    let best = null;
    for (const line of stdout.split('\n')) {
      if (!/"Pinky"/.test(line) || /审批|Gate/.test(line)) continue;
      const m = line.match(/(0x[0-9a-f]+)\s+.*?(\d+)x(\d+)\+(-?\d+)\+(-?\d+)/i);
      if (!m) continue;
      const w = Number(m[2]), h = Number(m[3]);
      if (w < 300 || h < 300) continue;
      const area = w * h;
      if (!best || area > best.area) best = { id: m[1], x: Number(m[4]), y: Number(m[5]), w, h, area };
    }
    return best;
  } catch { return null; }
}

async function winRect(id) {
  try {
    const { stdout } = await exec('xwininfo', ['-id', id], { timeout: 4000 });
    const n = (re) => { const m = stdout.match(re); return m ? Number(m[1]) : null; };
    const x = n(/Absolute upper-left X:\s+(-?\d+)/);
    const y = n(/Absolute upper-left Y:\s+(-?\d+)/);
    const w = n(/Width:\s+(\d+)/);
    const h = n(/Height:\s+(\d+)/);
    if (x === null || y === null || !w || !h) return null;
    return { x, y, w, h };
  } catch { return null; }
}

async function raiseWin(id) {
  if (!KEEP_FRONT) return;
  try { await exec('xdotool', ['windowraise', id], { timeout: 3000 }); } catch {}
}

// ---------------- 检测手机矩形 ----------------

/** 抓一张区域图（窗口像素） */
async function grabPNG(rect, out) {
  await exec('ffmpeg', [
    '-hide_banner', '-loglevel', 'error', '-y',
    '-f', 'x11grab', '-video_size', `${rect.w}x${rect.h}`,
    '-i', `${DISPLAY}.0+${Math.max(0, rect.x)},${Math.max(0, rect.y)}`,
    '-frames:v', '1', out,
  ], { timeout: 12000 });
}

async function detect(rect) {
  if (MANUAL) {
    const [x, y, w, h] = MANUAL.split(',').map(Number);
    if ([x, y, w, h].every(Number.isFinite)) return { x, y, w, h, method: 'manual' };
  }
  const tmp = `/tmp/pinky-probe-${process.pid}.png`;
  try {
    await grabPNG(rect, tmp);

    const SW = 270, SH = Math.round((270 * rect.h) / rect.w);
    const { stdout } = await exec('ffmpeg', [
      '-hide_banner', '-loglevel', 'error',
      '-i', tmp, '-vf', `scale=${SW}:${SH}`, '-f', 'rawvideo', '-pix_fmt', 'rgb24', '-',
    ], { timeout: 12000, maxBuffer: 64 << 20 });

    const buf = Buffer.from(stdout);
    if (buf.length < SW * SH * 3) return null;
    const at = (x, y) => { const i = (y * SW + x) * 3; return [buf[i], buf[i + 1], buf[i + 2]]; };

    // 背景亮度：取右侧栏区域（Pinky 右侧是任务栏，偏白）与四角中位数
    const samples = [];
    for (const [x, y] of [[2, 2], [SW - 3, 2], [2, SH - 3], [SW - 3, SH - 3], [SW - 10, SH / 2]]) {
      samples.push(at(Math.floor(x), Math.floor(y)));
    }
    const lum = (p) => 0.299 * p[0] + 0.587 * p[1] + 0.114 * p[2];
    const bgLum = samples.map(lum).sort((a, b) => a - b)[Math.floor(samples.length / 2)];

    // 内容像素：与背景亮度或色差明显不同
    const isContent = (p) => Math.abs(lum(p) - bgLum) > 28
      || Math.abs(p[0] - p[2]) > 40;

    const col = new Array(SW).fill(0), row = new Array(SH).fill(0);
    for (let y = 0; y < SH; y++) for (let x = 0; x < SW; x++) {
      if (isContent(at(x, y))) { col[x]++; row[y]++; }
    }

    // 找连续的高占比列区间（手机屏幕是一根很「满」的竖条）
    const run = (arr, len, thr, maxGap) => {
      let i0 = -1, i1 = -1, gap = 0;
      for (let i = 0; i < arr.length; i++) {
        if (arr[i] / len > thr) { if (i0 < 0) i0 = i; i1 = i; gap = 0; }
        else if (i0 >= 0) { gap++; if (gap > maxGap) break; i1 = i; }
      }
      return i0 < 0 ? null : [i0, i1];
    };

    const cx = run(col, SH, 0.45, 6);
    if (!cx) return null;

    const rowIn = new Array(SH).fill(0);
    for (let y = 0; y < SH; y++) {
      let d = 0;
      for (let x = cx[0]; x <= cx[1]; x++) if (isContent(at(x, y))) d++;
      rowIn[y] = d;
    }
    const cy = run(rowIn, cx[1] - cx[0] + 1, 0.55, 6);
    if (!cy) return null;

    const sx = rect.w / SW, sy = rect.h / SH;
    const x = Math.round(cx[0] * sx), w = Math.round((cx[1] - cx[0] + 1) * sx);
    const y = Math.round(cy[0] * sy), h = Math.round((cy[1] - cy[0] + 1) * sy);
    if (w < 100 || h < 180) return null;

    // 手机白边内收
    const px = Math.round(w * 0.014), py = Math.round(h * 0.010);
    return { x: x + px, y: y + py, w: Math.max(60, w - px * 2), h: Math.max(120, h - py * 2), method: 'auto' };
  } catch (e) {
    stats.lastError = String(e.message || e).slice(0, 160);
    stats.detectFails++;
    return null;
  }
}

// ---------------- 抓帧 ----------------

function stopGrab() {
  if (grab) { try { grab.kill('SIGKILL'); } catch {} grab = null; }
}

function startGrab(g) {
  stopGrab();
  const outW = Math.min(TARGET_W, g.w);
  const vf = `crop=${g.w}:${g.h}:${g.relX}:${g.relY},scale=${outW}:-2:flags=bicubic`;

  grab = spawn('ffmpeg', [
    '-hide_banner', '-loglevel', 'error',
    '-f', 'x11grab', '-framerate', String(FPS),
    '-video_size', `${g.winW}x${g.winH}`,
    '-i', `${g.display}.0+${g.winX},${g.winY}`,
    '-vf', vf, '-f', 'mjpeg', '-q:v', '4', '-',
  ], { stdio: ['ignore', 'pipe', 'pipe'] });

  let acc = Buffer.alloc(0);
  const SOI = Buffer.from([0xff, 0xd8]), EOI = Buffer.from([0xff, 0xd9]);
  grab.stdout.on('data', (chunk) => {
    acc = acc.length ? Buffer.concat([acc, chunk]) : chunk;
    for (let n = 0; n < 64; n++) {
      const s = acc.indexOf(SOI);
      if (s < 0) { if (acc.length > 1 << 20) acc = Buffer.alloc(0); return; }
      if (s > 0) acc = acc.subarray(s);
      const e = acc.indexOf(EOI, 2);
      if (e < 0) return;
      frame = Buffer.from(acc.subarray(0, e + 2));
      acc = acc.subarray(e + 2);
      seq++; stats.frames++; stats.lastFrameAt = Date.now();
      for (const send of viewers) { try { send(frame); } catch {} }
    }
  });
  grab.stderr.on('data', (d) => {
    const s = String(d).trim();
    if (s) { stats.lastError = s.slice(0, 200); stats.errors++; }
  });
  grab.on('exit', (code) => { if (code && code > 0) { stats.lastError = `ffmpeg exit ${code}`; stats.errors++; } });
  stats.restarts++;
}

// ---------------- 监督 ----------------

async function supervise() {
  try {
    let w = await findWin();
    if (!w && winId) { const r = await winRect(winId); if (r) w = { id: winId, ...r }; }
    if (!w) { geom = null; return; }
    winId = w.id;

    const r = await winRect(w.id);
    if (!r) { geom = null; return; }

    const same = geom && geom.winId === winId && geom.winX === r.x && geom.winY === r.y
      && geom.winW === r.w && geom.winH === r.h && grab && grab.exitCode === null;
    if (same) return;

    await raiseWin(winId);
    const d = await detect(r) || lastGood;
    if (!d) { geom = null; return; }
    lastGood = d;
    geom = { winId, winX: r.x, winY: r.y, winW: r.w, winH: r.h, display: DISPLAY,
             relX: d.x, relY: d.y, w: d.w, h: d.h, method: d.method };
    startGrab(geom);
  } finally {
    setTimeout(supervise, 2500);
  }
}

// ---------------- HTTP ----------------

const HDR = (n) => Buffer.concat([
  Buffer.from(`\r\n--pinkyframe\r\nContent-Type: image/jpeg\r\nContent-Length: ${n}\r\n\r\n`),
]);

const server = http.createServer((req, res) => {
  const u = new URL(req.url, 'http://x');
  res.setHeader('Access-Control-Allow-Origin', '*');
  res.setHeader('Access-Control-Allow-Headers', '*');
  res.setHeader('Cache-Control', 'no-store');

  if (u.pathname === '/health' || u.pathname === '/status') {
    const age = stats.lastFrameAt ? Date.now() - stats.lastFrameAt : -1;
    res.writeHead(200, { 'Content-Type': 'application/json; charset=utf-8' });
    res.end(JSON.stringify({
      live: !!geom && age >= 0 && age < 2500,
      seq, geom,
      stats: { ...stats, frameAgeMs: age, viewers: viewers.size, fps: FPS },
    }));
    return;
  }

  if (u.pathname === '/frame.jpg') {
    if (!frame.length) { res.writeHead(503); res.end('no frame'); return; }
    res.writeHead(200, { 'Content-Type': 'image/jpeg' });
    res.end(frame);
    return;
  }

  if (u.pathname === '/stream') {
    res.writeHead(200, {
      'Content-Type': 'multipart/x-mixed-replace; boundary=pinkyframe',
      'Connection': 'keep-alive', Pragma: 'no-cache',
    });
    const send = (f) => { if (!res.writableEnded) res.write(Buffer.concat([HDR(f.length), f])); };
    viewers.add(send);
    if (frame.length) send(frame);
    req.on('close', () => viewers.delete(send));
    return;
  }

  res.writeHead(404, { 'Content-Type': 'text/plain; charset=utf-8' });
  res.end('pinky-bridge: /health /frame.jpg /stream');
});

server.listen(PORT, HOST, () => {
  console.log(`[pinky-bridge] http://${HOST}:${PORT} display=${DISPLAY} fps=${FPS} w=${TARGET_W}`);
  supervise();
});
for (const s of ['SIGINT', 'SIGTERM']) process.on(s, () => { stopGrab(); server.close(); process.exit(0); });
