// plain.js: one shared vocabulary. Engine ids and technical terms become words a person would say.
const WORD = { temp: "Temperature", sat: "Saturation", lum: "Luminance", hue: "Hue", gamma: "Gamma", gain: "Gain", lift: "Lift", offset: "Offset", master: "Master", r: "Red", g: "Green", b: "Blue", y: "Luma", tint: "Tint", exp: "Exposure", cdl: "CDL" };
const HUES = ["red", "orange", "yellow", "green", "cyan", "blue", "purple", "pink"];
const cap = (w) => (WORD[w.toLowerCase()] || w.charAt(0).toUpperCase() + w.slice(1));
/** lift_master -> "Lift · Master", temp_slider -> "Temperature slider", "0 0-7, 8 px per click" -> "Hue 0 (red), 8 px per click" */
export function plain(text) {
    let t = String(text == null ? "" : text);
    if (/^[a-z]+(, [a-z]+)+$/.test(t)) return t.split(", ").map(cap).join(" \u00b7 ");
    t = t.replace(/\b(\d) 0-7\b/g, (m, n) => `Hue ${n} (${HUES[+n] || "colour"})`);
    t = t.replace(/\b([a-z]+)_slider\b/g, (m, a) => `${cap(a)} slider`);
    t = t.replace(/\b[a-z]+(?:_[a-z]+)+\b/g, (m) => m.split("_").map(cap).join(" · "));
    t = t.replace(/\bper click\b/g, "per notch");
    t = t.replace(/\b1 (frame|line|second|minute|unit|click|notch|step|layer|clip|marker|pixel|px)s\b/g, "1 $1"); // "1 frames" from the engine summary
    return t;
}
/** unit chip text */
export const plainUnit = (u) => String(u).replace(/^units per click$/, "amount per notch").replace(/ per click$/, " per notch");
/** risk line (a command) -> a sentence, and whether it is destructive */
export function plainCommand(line) {
    const m0 = /^([\w.-]+)\.([\w-]+):\s*/.exec(String(line)); const where = m0 ? `${m0[1].charAt(0).toUpperCase() + m0[1].slice(1)} \u00b7 ${m0[2]}` : "";
    const cmd = String(line).replace(/^[\w.-]+\.[\w-]+:\s*/, "");
    const rm = /\brm\s+(?:-[a-zA-Z]*[rf][a-zA-Z]*\s+)+(['"]?)(\S+?)\1(?:\s|$)/.exec(cmd);
    if (rm) { const where = /Library/.test(rm[2]) ? "a folder in your Library" : /^~|^\/Users\//.test(rm[2]) ? "a folder in your home folder" : "a folder"; return { text: `Deletes ${where}`, raw: cmd, destructive: true, where }; }
    if (/\bsudo\b/.test(cmd)) return { text: "Runs a program with administrator rights", raw: cmd, destructive: true, where };
    if (/\b(mkfs|dd|shutdown|reboot|killall|pkill)\b/.test(cmd)) return { text: "Runs a program that can stop or erase things", raw: cmd, destructive: true, where };
    return { text: "Runs a program", raw: cmd, destructive: false, where };
}

/** control ids as printed on the deck: P1btn -> "P1 button", ScreenMode -> "Screen Mode" */
export const plainControl = (t) => String(t == null ? "" : t).replace(/^P(\d)btn$/, "P$1 button").replace(/([a-z])([A-Z])/g, "$1 $2");

const KIND_NAME = { knob: "knob", dial: "dial", slider: "slider", button: "button", key: "key" };
/** deck legend not printed (D2, C3, L1...): "D2 knob" instead of the bare id */
export const titleFor = (label, kind) => { const t = plainControl(label); return /^[A-Z]\d?[A-Za-z]?\d*$/.test(t) && /\d/.test(t) && KIND_NAME[kind] ? `${t} ${KIND_NAME[kind]}` : t; };

/** the engine's history labels ("Set ControlDial base", "Clear D1", "Add app X") -> plain words for toasts and the history list.
 *  nameOf(id) turns a control id into the name printed on the deck; unknown shapes pass through unchanged. */
/* the engine's "Restore defaults" label does not say which app: the editor remembers which app it restored and when, and names it in history rows and the undo toast */
const restoreLog = [];
export function noteRestore(app, t = Date.now()) { restoreLog.push({ t, app }); if (restoreLog.length > 40) restoreLog.shift(); }
export function restoreApp(t) { let best = null; for (const r of restoreLog) { const d = t == null ? 0 : Math.abs(r.t - t); if ((t == null || d < 30000) && (!best || d <= best.d)) best = { d, app: r.app }; } return best ? best.app : null; }
export function plainChange(label, nameOf = plainControl, appHint) {
    const t = String(label == null ? "" : label).trim(); let m;
    const words = (k) => String(k).replace(/([a-z])([A-Z])/g, "$1 $2").replace(/[_.-]+/g, " ").toLowerCase().replace(/^sens$/, "sensitivity");
    if ((m = /^Set setting (.+)$/.exec(t))) return `Changed the ${words(m[1])} setting`;
    if ((m = /^Set (\S+) (\S+)$/.exec(t))) { const k = m[2]; return /^(base|action|app|binding)$/i.test(k) ? `Changed ${nameOf(m[1])}` : `Changed ${nameOf(m[1])}: ${words(k)}`; }
    if ((m = /^Assign (\S+)$/.exec(t))) return `Changed the action on ${nameOf(m[1])}`; // the engine does not say which action; the toast, the undo toast and the history all use this one line
    if ((m = /^Clear (\S+)$/.exec(t))) return `Unassigned ${nameOf(m[1])}`;
    if ((m = /^Restore (.+) defaults$/.exec(t))) return `Restored ${m[1]} defaults`;
    if ((m = /^Restore defaults(?:: (?:bank )?(\S+))?$/.exec(t))) { const of = appHint ? ` for ${appHint}` : ""; return m[1] ? `Restored ${/^[a-z-]+$/.test(m[1]) ? m[1] + " bank" : nameOf(m[1])} defaults${of}` : appHint ? `Restored ${appHint} defaults` : "Restored defaults"; }
    if ((m = /^Add app (.+) \(template\)$/.exec(t))) return `Added ${m[1]} from the standard preset`;
    if ((m = /^Add app (.+)$/.exec(t))) return `Added ${m[1]}`;
    if ((m = /^Remove app (.+)$/.exec(t))) return `Removed ${m[1]}`;
    if (t === "Copy controls") return "Copied controls";
    return t;
}
