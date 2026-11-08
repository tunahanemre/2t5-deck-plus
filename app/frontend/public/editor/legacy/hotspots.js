// hotspots.js — hotspot table for the deck image (assets/deck-cutout.png, 1500x1500).
// Positions are photo pixels, calibrated by eye with ?calib=1. Keyed by the device-map control ids.
// Format: id: [cx, cy, w, h, shape]. The deck body occupies about x 33-1467, y 470-1028.
export const PHOTO = { w: 1500, h: 1500, src: "../assets/deck-cutout.png", viewBox: "20 455 1460 585" };
const K = (x, y) => [x, y, 70, 55, "rect"];      // square key
const R = (x, y, r = 16) => [x, y, r * 2, r * 2, "circle"];
const KNOB = (x, y) => R(x, y, 33);
const XS = [547, 623, 700, 776, 852, 928, 1004, 1080];
export const HOTSPOTS = {
    Undo: K(118, 545), Redo: K(118, 607), CustomMode: K(286, 545), ClrBW: K(370, 545), Hue: K(453, 545), Sat: K(453, 607), Lum: K(453, 668),
    Shift: K(118, 895), Ctrl: K(203, 895), Cmd: K(286, 895), Alt: K(370, 895), Tab: K(453, 895),
    Copy: K(118, 953), Paste: K(203, 953), C1: K(370, 953), C2: K(453, 953), Col: K(119, 828),
    BeforeAfter: K(1215, 545), ScreenMode: K(1299, 545), Export: K(1382, 545),
    Up: K(1299, 890), Left: K(1216, 953), Down: K(1299, 953), Right: K(1382, 953),
    L1: R(119, 665), L2: R(119, 718), L3: R(119, 770), Fn: R(286, 953), C3: R(777, 756), C4: R(852, 756), C5: R(1216, 890), C6: R(1382, 890),
    D1: KNOB(453, 780), Contrast: KNOB(583, 823), Clarity: KNOB(714, 823), Shadows: KNOB(914, 823), Highlights: KNOB(1044, 823),
    Exposure: KNOB(649, 925), Blacks: KNOB(849, 925), Whites: KNOB(979, 925), D2: KNOB(1110, 925),
    Temperature: KNOB(1241, 652), Tint: KNOB(1372, 652), Vibrance: KNOB(1175, 780), Saturation: KNOB(1306, 780),
    ControlDial: R(283, 718, 62),
};
XS.forEach((x, i) => { HOTSPOTS["P" + (i + 1) + "btn"] = R(x, 534, 17); HOTSPOTS["P" + (i + 1)] = [x, 633, 34, 123, "bar"]; });

/** Same shape as the DECKMAP profile, but with photo-pixel rects. */
export function photoProfile(base) {
    const rects = {};
    for (const [id, [cx, cy, w, h, shape]] of Object.entries(HOTSPOTS)) rects[id] = { x: cx - w / 2, y: cy - h / 2, w, h, shape };
    return { ...base, layout: { canvas: { w: PHOTO.w, h: PHOTO.h }, rects } };
}

/** Swap the schematic plate for the photo and crop the viewBox to the deck body. */
export function applyPhoto(svg, calib) {
    svg.setAttribute("viewBox", PHOTO.viewBox);
    svg.classList.add("photo");
    if (calib) svg.classList.add("calib");
    const img = document.createElementNS("http://www.w3.org/2000/svg", "image");
    img.setAttribute("href", PHOTO.src);
    img.setAttribute("x", 0); img.setAttribute("y", 0); img.setAttribute("width", PHOTO.w); img.setAttribute("height", PHOTO.h);
    img.setAttribute("class", "deck-photo");
    img.setAttribute("pointer-events", "none");
    svg.insertBefore(img, svg.firstChild);
}
