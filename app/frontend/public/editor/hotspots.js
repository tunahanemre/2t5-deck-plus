// hotspots.js: one authored shape per control on the real Loupedeck+ photo (assets/deck-cutout.png, 1500x1500).
// The same shape drives the hit area and the edge-light, so the line sits on the part's real rim.
// Row format: [cx, cy, w, h, shape, rx]   (photo pixels; shape = circle | rrect | stadium)
// Calibrate with ?calib=1 (draws every candidate outline as a 1px magenta stroke; check at 2x zoom).
export const PHOTO = { w: 1500, h: 1500, src: "./assets/deck-cutout.png", src2x: "./assets/deck-cutout-2x.png", viewBox: [26, 460, 1448, 576] };

/* Measured against the 2x cut-out (overlay sheets at 5-7x, 2026-11-04): the outline traces the key's whole silhouette, the dark side wall on the right and the front lip below included (the old shape stopped at the top face, 66x51, and sat 3-5 px off on the lower rows). The lighting is from the top left, so the silhouette is wider on the right and the rows differ a little. */
const KEY = (x, y) => (y > 920 ? [x + 1.8, y - 0.5, 74, 53, "rrect", 8] : y > 850 ? [x + 1.8, y - 3.5, 74, 54, "rrect", 8] : [x + 1.5, y + 1, 72, 53, "rrect", 8]);
const BTN = (x, y, d = 32) => [x, y, d, d, "circle", d / 2];
const KNOB = (x, y) => [x + 1.5, y + 1.5, 63, 63, "circle", 31.5]; // the knob body (rim to rim, shadow outside); the old 66 ring overhung the lit side by 2 px
const XS = [547, 623, 700, 776, 852, 928, 1004, 1080];

const T = {
    Undo: KEY(118, 545), Redo: KEY(118, 607), CustomMode: KEY(286, 545), ClrBW: KEY(370, 545), Hue: KEY(453, 545), Sat: KEY(453, 607), Lum: KEY(453, 668),
    Shift: KEY(118, 895), Ctrl: KEY(203, 895), Cmd: KEY(286, 895), Alt: KEY(370, 895), Tab: KEY(453, 895),
    Copy: KEY(118, 953), Paste: KEY(203, 953), C1: KEY(370, 953), C2: KEY(453, 953), Col: KEY(119, 828),
    BeforeAfter: KEY(1215, 545), ScreenMode: KEY(1299, 545), Export: KEY(1382, 545),
    Up: KEY(1299, 890), Left: KEY(1216, 953), Down: KEY(1299, 953), Right: KEY(1382, 953),
    L1: BTN(119, 665), L2: BTN(119, 718), L3: BTN(119, 770), Fn: BTN(286, 953), C3: BTN(777, 756), C4: BTN(852, 756), C5: BTN(1216, 890), C6: BTN(1382, 890),
    D1: KNOB(453, 780), Contrast: KNOB(583, 823), Clarity: KNOB(714, 823), Shadows: KNOB(914, 823), Highlights: KNOB(1044, 823),
    Exposure: KNOB(649, 951), Blacks: KNOB(849, 951), Whites: KNOB(979, 951), D2: KNOB(1110, 951),
    Temperature: KNOB(1241, 652), Tint: KNOB(1372, 652), Vibrance: KNOB(1175, 780), Saturation: KNOB(1306, 780),
    ControlDial: [283, 718, 124, 124, "circle", 62],
};
XS.forEach((x, i) => { T["P" + (i + 1) + "btn"] = BTN(x, 534, 34); T["P" + (i + 1)] = [x, 633, 34, 123, "stadium", 17]; });

/** kind drives the live animation and the tooltip wording: key | button | knob | dial | slider */
function kindOf(id, shape) {
    if (id === "ControlDial") return "dial";
    if (shape === "stadium") return "slider";
    if (shape === "circle" && Math.abs(T[id][2] - 63) < 1) return "knob";
    if (shape === "circle") return "button";
    return "key";
}
export const HOT = {};
for (const [id, [cx, cy, w, h, shape, rx]] of Object.entries(T)) HOT[id] = { id, cx, cy, w, h, shape, rx, kind: kindOf(id, shape) };
export const hotJSON = () => JSON.stringify(HOT);

/** Row groups: callouts show only the focused group, so the wall of labels never appears. */
export const GROUPS = {
    topkeys: { label: "Top keys", ids: ["Undo", "Redo", "CustomMode", "ClrBW", "Hue", "Sat", "Lum", "BeforeAfter", "ScreenMode", "Export"], up: true },
    faders: { label: "Faders", ids: [...Array(8)].flatMap((_, i) => ["P" + (i + 1), "P" + (i + 1) + "btn"]).concat([]), up: true },
    knobs: { label: "Knobs", ids: ["D1", "ControlDial", "Contrast", "Clarity", "Shadows", "Highlights", "Temperature", "Tint", "Vibrance", "Saturation", "L1", "L2", "L3"] },
    lower: { label: "Lower knobs", ids: ["Exposure", "Blacks", "Whites", "D2", "C3", "C4"], up: false },
    modkeys: { label: "Modifier keys", ids: ["Shift", "Ctrl", "Cmd", "Alt", "Tab"], up: true },
    keys: { label: "Bottom keys", ids: ["Col", "Copy", "Paste", "Fn", "C1", "C2", "Up", "Left", "Down", "Right", "C5", "C6"], up: false },
};
export const GROUP_OF = {};
for (const [g, d] of Object.entries(GROUPS)) for (const id of d.ids) GROUP_OF[id] = g;

/** The eight slider dots printed under the faders, sampled from the photo (red, orange, yellow, green, cyan, blue, purple, magenta). Slider P1..P8 (and the button above each) use their own colour for every highlight. */
export const SLIDER_DOTS = [["red", "#fa6a60"], ["orange", "#fc8857"], ["yellow", "#fdf759"], ["green", "#8ee272"], ["cyan", "#8cf0ff"], ["blue", "#48abec"], ["purple", "#b78bea"], ["magenta", "#eb6fe1"]];
export const SLIDER_COLOR = {};
SLIDER_DOTS.forEach(([, c], i) => { SLIDER_COLOR["P" + (i + 1)] = c; SLIDER_COLOR["P" + (i + 1) + "btn"] = c; });

/** The Loupedeck+ LEDs that carry state: one left of Custom Mode, one right of each of Hue / Sat / Lum (photo px, measured on the unlit dots). The photo itself shows all four dark. */
export const LEDS = { custom: [230, 545], hue: [507, 548], sat: [509, 609], lum: [509, 671] };
