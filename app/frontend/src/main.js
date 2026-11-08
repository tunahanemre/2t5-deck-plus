// 2T5 DECK+ Status window (Task 5).
//
// Talks to the bound Go API in app/app.go via the Wails-generated bindings
// (app/frontend/wailsjs/go/main/App.js) and the "status" event emitted every
// 250ms by app.go's runStatusTicker (see app/app.go, statusEventInterval).
//
// Pattern: build the DOM once (index.html), then only patch textContent /
// classList / hidden on every status tick — never innerHTML-rebuild the
// whole tree. Avoids the "flash on rebuild" class of bugs documented from
// the Command Center SSE work (non-deterministic HTML / transition replay on
// DOM reconstruction). See frontend-developer memory: "SSE Anti-Flicker:
// Patch-Based DOM Updates".
import {
    GetStatus,
    StartEngine,
    StopEngine,
    RecheckPermissions,
    OpenAccessibilitySettings,
    OpenScreenRecordingSettings,
    ToggleHUD,
} from "../wailsjs/go/main/App.js";
import { EventsOn } from "../wailsjs/runtime/runtime.js";
import { installEditorBridge } from "./editorBridge.js";

// --- DOM refs (grabbed once) -------------------------------------------

const stateDotEl = document.getElementById("stateDot");
const subtitleEl = document.getElementById("subtitle");

const a11yBannerEl = document.getElementById("a11yBanner");
const a11yOpenBtn = document.getElementById("a11yOpenBtn");
const a11yRecheckBtn = document.getElementById("a11yRecheckBtn");

const srBannerEl = document.getElementById("srBanner");
const srOpenBtn = document.getElementById("srOpenBtn");
const srRecheckBtn = document.getElementById("srRecheckBtn");

const toggleBtn = document.getElementById("toggleBtn");
const controlHintEl = document.getElementById("controlHint");
const controlErrorEl = document.getElementById("controlError");
const hudToggleBtn = document.getElementById("hudToggleBtn");

const statEngineEl = document.getElementById("statEngine");
const statGateEl = document.getElementById("statGate");
const statWheelEl = document.getElementById("statWheel");
const statModeEl = document.getElementById("statMode");
const rowActiveClipEl = document.getElementById("rowActiveClip");
const statActiveClipEl = document.getElementById("statActiveClip");
const rowMarkerEl = document.getElementById("rowMarker");
const statMarkerEl = document.getElementById("statMarker");
const statProjectEl = document.getElementById("statProject");
const projectBadgeEl = document.getElementById("projectBadge");
const rowProjectEl = document.getElementById("rowProject");
const statResolveEl = document.getElementById("statResolve");
const statSidecarEl = document.getElementById("statSidecar");
const sidecarErrorEl = document.getElementById("sidecarError");

const midiCurrentEl = document.getElementById("midiCurrent");
const midiHistoryEl = document.getElementById("midiHistory");

// --- Local state ----------------------------------------------------------

// Last status object rendered (engine.Status shape, PascalCase, no json
// tags). Used so a Recheck-permissions click can patch just the two
// permission fields and re-render without waiting for the next 250ms tick.
let lastStatus = null;

// True while a Start/Stop call is in flight — guards the toggle button
// against double-clicks and against the periodic "status" event stomping on
// the transient "Starting…"/"Stopping…" label mid-request.
let isBusy = false;

// Rolling client-side MIDI activity trail. Most-recent first, deduped
// against consecutive-identical values (the status ticker fires every
// 250ms regardless of whether new MIDI arrived, so a naive push-every-tick
// list would just show 8 copies of the same value while a knob is idle).
const MIDI_HISTORY_MAX = 8;
let midiHistory = [];

// --- Helpers ----------------------------------------------------------

function isGuardedProject(name) {
    // SAFETY badge disabled per user request (2026-07-02). To restore, return:
    //   const n = (name || "").toLowerCase();
    //   return n.includes("dodee") || n.includes("most");
    return false;
}

function errMsg(err) {
    if (!err) return "Unknown error.";
    if (typeof err === "string") return err;
    if (err instanceof Error && err.message) return err.message;
    if (typeof err === "object" && err.message) return err.message;
    try {
        return JSON.stringify(err);
    } catch {
        return String(err);
    }
}

function clearControlError() {
    controlErrorEl.hidden = true;
    controlErrorEl.textContent = "";
}

function showControlError(err) {
    controlErrorEl.hidden = false;
    controlErrorEl.textContent = errMsg(err);
}

// --- Render -------------------------------------------------------------

/**
 * Patch the DOM from a Status snapshot. Called from:
 *  - the initial GetStatus() on load
 *  - every "status" event (~4/sec)
 *  - a merged copy after RecheckPermissions() resolves (immediate feedback,
 *    doesn't wait for the next tick)
 */
function render(status) {
    lastStatus = status;

    const running = !!status.Running;
    const gateAllow = !!status.GateAllow;
    const a11yOK = !!status.AccessibilityOK;
    const srOK = !!status.ScreenRecordingOK;

    // --- header: state dot + subtitle ---
    let state = "off";
    if (running) state = gateAllow ? "ok" : "warn";
    stateDotEl.className = "state-dot state-dot--" + state;

    if (!running) {
        subtitleEl.textContent = "Engine stopped";
        stateDotEl.setAttribute("aria-label", "Engine stopped");
    } else if (gateAllow) {
        subtitleEl.textContent = "Engine running — drags active";
        stateDotEl.setAttribute("aria-label", "Engine running, gate open");
    } else {
        subtitleEl.textContent = "Engine running — gate blocked (Resolve not frontmost / not on Color page)";
        stateDotEl.setAttribute("aria-label", "Engine running, gate blocked");
    }

    // --- permission banners ---
    a11yBannerEl.hidden = a11yOK;
    srBannerEl.hidden = srOK;

    // --- Start/Stop control (skipped mid-request so the busy label sticks) ---
    if (!isBusy) {
        toggleBtn.textContent = running ? "Stop Engine" : "Start Engine";
        toggleBtn.classList.toggle("btn-toggle--stop", running);
        toggleBtn.classList.toggle("btn-toggle--start", !running);

        const blockedByPerm = !running && !a11yOK;
        toggleBtn.disabled = blockedByPerm;
        controlHintEl.hidden = !blockedByPerm;
        if (blockedByPerm) {
            controlHintEl.textContent = "Grant Accessibility permission above to enable Start.";
        }
    }

    // --- status grid ---
    statEngineEl.textContent = running ? "Running" : "Stopped";
    statEngineEl.classList.toggle("stat-value--ok", running);
    statEngineEl.classList.toggle("stat-value--muted", !running);

    statGateEl.textContent = gateAllow ? "Allowing" : "Blocked";
    statGateEl.classList.toggle("stat-value--ok", gateAllow);
    statGateEl.classList.toggle("stat-value--warn", !gateAllow);

    statWheelEl.textContent = status.ActiveWheel || "—";
    statModeEl.textContent = status.Mode || "—";

    // D1 "clip-select" target -- only shown once populated (Edit page +
    // D1 has been turned at least once). Resolve has no API to highlight
    // a clip on-screen, so this row IS the visual feedback for which
    // clip the property/reset dials currently affect.
    const activeClip = status.ActiveClip || "";
    rowActiveClipEl.hidden = !activeClip;
    statActiveClipEl.textContent = activeClip || "—";

    // C1 "marker mode" (create/resize/color a marker via ControlDial +
    // P1-8btn) -- only shown once active (a C1 press on the Edit page),
    // same "hidden until populated" pattern as the ActiveClip row above.
    // Resolve has no marker-selection-highlight API either, so this row
    // IS the visual feedback for which marker ControlDial/P1-8btn
    // currently target.
    const markerMode = status.MarkerMode || "";
    rowMarkerEl.hidden = !markerMode;
    statMarkerEl.textContent = markerMode || "—";

    const projectName = status.Project || "";
    statProjectEl.textContent = projectName || "—";
    const guarded = isGuardedProject(projectName);
    projectBadgeEl.hidden = !guarded;
    statProjectEl.classList.toggle("stat-value--warn", guarded);
    rowProjectEl.classList.toggle("stat-row--warn", guarded);

    const page = status.Page || "—";
    statResolveEl.textContent = (status.ResolveFrontmost ? "Frontmost" : "Background") + " · " + page;

    const sidecarConnected = !!status.SidecarConnected;
    statSidecarEl.textContent = sidecarConnected ? "Connected" : "Disconnected";
    statSidecarEl.classList.toggle("stat-value--ok", sidecarConnected);
    statSidecarEl.classList.toggle("stat-value--warn", !sidecarConnected);

    const showSidecarError = !sidecarConnected && !!status.SidecarError;
    sidecarErrorEl.hidden = !showSidecarError;
    sidecarErrorEl.textContent = showSidecarError ? status.SidecarError : "";

    // --- MIDI monitor ---
    midiCurrentEl.textContent = status.LastMIDI || "—";
    pushMidi(status.LastMIDI);
}

function pushMidi(value) {
    if (!value) return;
    if (midiHistory[0] === value) return; // no change since last tick
    midiHistory.unshift(value);
    if (midiHistory.length > MIDI_HISTORY_MAX) midiHistory.length = MIDI_HISTORY_MAX;
    renderMidiHistory();
}

function renderMidiHistory() {
    const frag = document.createDocumentFragment();
    midiHistory.forEach((value, i) => {
        const li = document.createElement("li");
        li.className = "midi-item" + (i === 0 ? " midi-item--latest" : "");
        li.textContent = value;
        frag.appendChild(li);
    });
    midiHistoryEl.replaceChildren(frag);
}

// --- Event handlers -------------------------------------------------------

async function handleToggle() {
    if (isBusy || toggleBtn.disabled) return;
    clearControlError();

    const wasRunning = !!(lastStatus && lastStatus.Running);
    isBusy = true;
    toggleBtn.disabled = true;
    toggleBtn.textContent = wasRunning ? "Stopping…" : "Starting…";

    try {
        if (wasRunning) {
            await StopEngine();
        } else {
            await StartEngine();
        }
    } catch (err) {
        showControlError(err);
    } finally {
        isBusy = false;
        // Fetch a fresh snapshot immediately rather than waiting up to
        // 250ms for the next ticker event, so the button/grid reflect the
        // real outcome right away.
        try {
            const fresh = await GetStatus();
            render(fresh);
        } catch (err) {
            console.error("[2t5-deck-plus] GetStatus after toggle failed:", err);
        }
    }
}

// handleHudToggle flips the native HUD status-monitor panel via
// app.ToggleHUD (see app/app.go). The Loupedeck C6 button reaches the exact
// same underlying toggle independently (via the engine's injected callback),
// so this button's label is only ever a best-effort reflection of the
// LAST-KNOWN state from whichever source toggled it most recently through
// THIS window's own clicks -- there is no live "is the HUD visible" event
// to subscribe to (monitor-only feature, v1 has no such polling).
let hudBusy = false;

function renderHudButton(visible) {
    hudToggleBtn.textContent = visible ? "Hide HUD" : "Show HUD";
    hudToggleBtn.classList.toggle("btn-hud--active", !!visible);
}

async function handleHudToggle() {
    if (hudBusy) return;
    hudBusy = true;
    hudToggleBtn.disabled = true;
    try {
        const visible = await ToggleHUD();
        renderHudButton(visible);
    } catch (err) {
        console.error("[2t5-deck-plus] ToggleHUD failed:", err);
    } finally {
        hudBusy = false;
        hudToggleBtn.disabled = false;
    }
}

async function handleRecheck(kind, btn) {
    if (btn.disabled) return;
    const original = btn.textContent;
    btn.disabled = true;
    btn.textContent = "Checking…";
    try {
        const perms = await RecheckPermissions();
        if (lastStatus) {
            const merged = { ...lastStatus };
            merged.AccessibilityOK = !!perms.Accessibility;
            merged.ScreenRecordingOK = !!perms.ScreenRecording;
            render(merged);
        }
    } catch (err) {
        console.error(`[2t5-deck-plus] RecheckPermissions (${kind}) failed:`, err);
    } finally {
        btn.disabled = false;
        btn.textContent = original;
    }
}

function wireHandlers() {
    toggleBtn.addEventListener("click", handleToggle);
    a11yOpenBtn.addEventListener("click", () => OpenAccessibilitySettings());
    srOpenBtn.addEventListener("click", () => OpenScreenRecordingSettings());
    a11yRecheckBtn.addEventListener("click", () => handleRecheck("accessibility", a11yRecheckBtn));
    srRecheckBtn.addEventListener("click", () => handleRecheck("screen-recording", srRecheckBtn));
    hudToggleBtn.addEventListener("click", handleHudToggle);
}

// --- Init -------------------------------------------------------------

async function init() {
    wireHandlers();

    try {
        const initial = await GetStatus();
        render(initial);
    } catch (err) {
        console.error("[2t5-deck-plus] initial GetStatus failed:", err);
    }

    EventsOn("status", (status) => render(status));
}

init();


/* ------------------------------------------------------------------
   Status | Editor tabs. The Editor is a standalone page (public/editor,
   served at ./editor/index.html) shown in an iframe; the window grows
   for it and shrinks back when returning to Status. Wails runtime calls
   are guarded so the page also works in a plain browser.
------------------------------------------------------------------- */
(function initTabs() {
    const tabStatus = document.getElementById("tabStatus");
    const tabEditor = document.getElementById("tabEditor");
    const statusView = document.getElementById("statusView");
    const editorView = document.getElementById("editorView");
    const frame = document.getElementById("editorFrame");
    if (!tabStatus || !tabEditor) return;
    installEditorBridge(frame); // the editor iframe asks this page to make its bound Go calls (writes)
    function show(editor) {
        tabStatus.setAttribute("aria-selected", String(!editor));
        tabEditor.setAttribute("aria-selected", String(editor));
        statusView.hidden = editor;
        editorView.hidden = !editor;
        document.body.classList.toggle("is-editor", editor);
        if (editor && !frame.getAttribute("src")) frame.setAttribute("src", frame.dataset.src);
        try {
            if (window.runtime && typeof window.runtime.WindowSetSize === "function") {
                if (editor) window.runtime.WindowSetSize(1360, 860);
                else window.runtime.WindowSetSize(720, 560);
            }
        } catch (e) { /* not running under Wails */ }
    }
    tabStatus.addEventListener("click", () => show(false));
    tabEditor.addEventListener("click", () => show(true));
    // Tunahan 2026-11-04 "no need old ld-resolve layout in the app anymore": the app opens straight into the Editor, the old Status page and the tab bar are not shown
    document.querySelector(".tabbar").hidden = true;
    show(true);
})();
