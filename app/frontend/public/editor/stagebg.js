/* The stage background drawn with per-pixel dithering (a plain CSS gradient shows visible banding).
   A CSS radial gradient this dark is quantised to ~16 grey steps before anything is composited on top, so the steps show as rings;
   here every device pixel gets the exact float value plus sub-level noise before rounding, so there are no steps to see.
   Same shape as the old CSS: ellipse 70% x 60% of the stage at 50% / 42%, --stage-lift at the centre to --stage at 72% of the ray. */
const css = (name) => getComputedStyle(document.documentElement).getPropertyValue(name).trim();
function rgb(c) {
    const m = /^#([0-9a-f]{6})$/i.exec(c);
    if (m) { const n = parseInt(m[1], 16); return [(n >> 16) & 255, (n >> 8) & 255, n & 255]; }
    const r = /rgba?\(([^)]+)\)/.exec(c); if (r) return r[1].split(",").slice(0, 3).map((v) => parseFloat(v));
    return [10, 10, 11];
}
export function initStageBg(stage) {
    if (!stage) return;
    const cv = document.createElement("canvas"); cv.className = "stage-bg"; cv.setAttribute("aria-hidden", "true");
    stage.prepend(cv); stage.classList.add("has-bg");
    let w0 = 0, h0 = 0, t = 0;
    const draw = () => {
        const dpr = Math.min(2, window.devicePixelRatio || 1), W = Math.max(1, Math.round(stage.clientWidth * dpr)), H = Math.max(1, Math.round(stage.clientHeight * dpr));
        if (W === w0 && H === h0) return; w0 = W; h0 = H;
        cv.width = W; cv.height = H;
        const ctx = cv.getContext("2d"); if (!ctx) return;
        const img = ctx.createImageData(W, H), d = img.data;
        const A = rgb(css("--stage-lift") || "#1A1A1E"), B = rgb(css("--stage") || "#0A0A0B");
        const cx = W * 0.5, cy = H * 0.42, rx = W * 0.7, ry = H * 0.6, end = 0.72;
        let seed = 0x2f6e2b1;
        const rnd = () => { seed ^= seed << 13; seed ^= seed >>> 17; seed ^= seed << 5; return (seed >>> 0) / 4294967296; };
        for (let y = 0, i = 0; y < H; y++) {
            const dy = (y + 0.5 - cy) / ry, dy2 = dy * dy;
            for (let x = 0; x < W; x++, i += 4) {
                const dx = (x + 0.5 - cx) / rx;
                let f = Math.sqrt(dx * dx + dy2) / end; if (f > 1) f = 1;
                const n = rnd() + rnd() - 1; // triangular noise, +-1 level
                d[i] = A[0] + (B[0] - A[0]) * f + n + 0.5;
                d[i + 1] = A[1] + (B[1] - A[1]) * f + n + 0.5;
                d[i + 2] = A[2] + (B[2] - A[2]) * f + n + 0.5;
                d[i + 3] = 255;
            }
        }
        ctx.putImageData(img, 0, 0);
    };
    draw();
    new ResizeObserver(() => { clearTimeout(t); t = setTimeout(draw, 120); }).observe(stage);
    matchMedia("(resolution: 2dppx)").addEventListener?.("change", () => { w0 = 0; draw(); });
}
