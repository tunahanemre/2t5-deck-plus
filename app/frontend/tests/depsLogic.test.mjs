// Run with: node --test app/frontend/tests/   (Node 22.7 or later; no install step, no browser)
import test from "node:test";
import assert from "node:assert/strict";
import { sortItems, groupItems, requiredMissing, decide, afterDismiss, summarize, fixKind, needsFor } from "../public/editor/depsLogic.js";

const it = (id, o = {}) => ({ id, name: id, required: false, status: "ok", fixAction: "", informational: false, ...o });
const LIST = () => [
    it("shell", { informational: true, status: "info" }),
    it("py", { status: "missing" }),
    it("loupedeck", { required: true, status: "ok" }),
    it("acc", { required: true, status: "missing", fixAction: "open-accessibility-settings" }),
    it("scripting", { status: "unknown" }),
    it("other", { required: true, status: "unknown" }),
    it("screen", { status: "ok" }),
];

test("required items come first, missing before unknown before ok, informational last", () => {
    const ids = sortItems(LIST()).map((x) => x.id);
    assert.deepEqual(ids, ["acc", "other", "loupedeck", "py", "scripting", "screen", "shell"]);
});

test("sortItems is stable and does not touch its input", () => {
    const a = LIST(), copy = JSON.stringify(a);
    sortItems(a);
    assert.equal(JSON.stringify(a), copy);
});

test("groupItems splits required, optional and notes", () => {
    const g = groupItems(LIST());
    assert.deepEqual(g.required.map((x) => x.id), ["acc", "other", "loupedeck"]);
    assert.deepEqual(g.optional.map((x) => x.id), ["py", "scripting", "screen"]);
    assert.deepEqual(g.notes.map((x) => x.id), ["shell"]);
});

test("requiredMissing lists only required items whose status is missing (unknown is not missing)", () => {
    assert.deepEqual(requiredMissing(LIST()), ["acc"]);
    assert.deepEqual(requiredMissing([]), []);
    assert.deepEqual(requiredMissing(null), []);
});

test("first run opens the panel once, even when everything is fine", () => {
    const items = [it("acc", { required: true })];
    assert.equal(decide({ items, dismissed: {}, firstRun: true }).open, true);
    assert.equal(decide({ items, dismissed: {}, firstRun: true }).reason, "first-run");
    assert.equal(decide({ items, dismissed: {}, firstRun: false }).open, false);
});

test("a required item that is missing opens the panel", () => {
    const d = decide({ items: LIST(), dismissed: {}, firstRun: false });
    assert.equal(d.open, true);
    assert.equal(d.reason, "required-missing");
});

test("an optional item that is missing never opens the panel by itself", () => {
    const items = [it("acc", { required: true }), it("py", { status: "missing" })];
    assert.equal(decide({ items, dismissed: {}, firstRun: false }).open, false);
});

test("dismissed while it stays missing: no nagging", () => {
    const dismissed = afterDismiss(LIST(), {});
    assert.deepEqual(dismissed, { acc: "missing" });
    assert.equal(decide({ items: LIST(), dismissed, firstRun: false }).open, false);
});

test("dismissal is per state: ok then missing again brings the panel back", () => {
    let dismissed = afterDismiss(LIST(), {});
    const fixed = LIST().map((x) => (x.id === "acc" ? { ...x, status: "ok" } : x));
    const d1 = decide({ items: fixed, dismissed, firstRun: false });
    assert.equal(d1.open, false);
    assert.deepEqual(d1.dismissed, {}, "the record is dropped once the item is ok");
    dismissed = d1.dismissed;
    const broken = decide({ items: LIST(), dismissed, firstRun: false });
    assert.equal(broken.open, true);
    assert.deepEqual(broken.dismissed, {});
});

test("a second required item going missing re-opens the panel even if the first stays dismissed", () => {
    const dismissed = afterDismiss(LIST(), {}); // acc dismissed
    const worse = LIST().map((x) => (x.id === "loupedeck" ? { ...x, status: "missing" } : x));
    const d = decide({ items: worse, dismissed, firstRun: false });
    assert.equal(d.open, true);
    assert.deepEqual(d.fresh, ["loupedeck"]);
});

test("an item that vanishes from the list drops its dismissal", () => {
    const d = decide({ items: [it("acc", { required: true })], dismissed: { gone: "missing", acc: "missing" }, firstRun: false });
    assert.deepEqual(d.dismissed, {});
});

test("busy (another drawer or the tour is open) postpones the panel without recording a dismissal", () => {
    const d = decide({ items: LIST(), dismissed: {}, firstRun: true, busy: true });
    assert.equal(d.open, false);
    assert.deepEqual(d.dismissed, {});
    assert.equal(decide({ items: LIST(), dismissed: {}, firstRun: true, busy: false }).open, true);
});

test("a failed or empty answer never opens the panel", () => {
    assert.equal(decide({ items: null, dismissed: {}, firstRun: true }).open, false);
    assert.equal(decide({ items: [], dismissed: {}, firstRun: true }).open, false);
});

test("junk dismissal data is ignored", () => {
    for (const bad of [null, undefined, "x", 5, []]) {
        const d = decide({ items: LIST(), dismissed: bad, firstRun: false });
        assert.equal(d.open, true);
    }
});

test("afterDismiss records every required-missing item and keeps older records that still hold", () => {
    const items = LIST().map((x) => (x.id === "loupedeck" ? { ...x, status: "missing" } : x));
    assert.deepEqual(afterDismiss(items, { acc: "missing" }), { acc: "missing", loupedeck: "missing" });
});

test("summarize says it plainly", () => {
    assert.deepEqual(summarize(LIST()), { tone: "bad", text: "1 required thing is missing" });
    const two = LIST().map((x) => (x.id === "loupedeck" ? { ...x, status: "missing" } : x));
    assert.equal(summarize(two).text, "2 required things are missing");
    const fine = [it("acc", { required: true }), it("py", { status: "missing" })];
    assert.deepEqual(summarize(fine), { tone: "warn", text: "Everything required is in place. 1 optional thing is missing" });
    assert.deepEqual(summarize([it("acc", { required: true })]), { tone: "ok", text: "Everything checks out" });
    assert.deepEqual(summarize(null), { tone: "unknown", text: "Could not check this Mac" });
});

test("a required item that never worked on this Mac (wasOK false) does not re-open the panel; one that did (wasOK true) does", () => {
    const never = [it("loupedeck", { required: true, status: "missing", wasOK: false })];
    assert.equal(decide({ items: never, dismissed: {}, firstRun: false }).open, false);
    const lost = [it("acc", { required: true, status: "missing", wasOK: true })];
    const d = decide({ items: lost, dismissed: {}, firstRun: false });
    assert.equal(d.open, true);
    assert.deepEqual(d.fresh, ["acc"]);
    // and first run still shows everything, worked or not
    assert.equal(decide({ items: never, dismissed: {}, firstRun: true }).reason, "first-run");
});

test("needsFor lists the missing items that carry a feature tag, in the catalog's own words", () => {
    const items = [
        it("scripting", { features: ["resolve"], status: "missing", name: "Resolve: External scripting set to Local" }),
        it("py", { features: ["resolve"], status: "ok" }),
        it("fin", { features: ["finder-tags"], status: "unknown" }),
        it("scr", { features: ["calibration"], status: "missing", name: "Screen Recording permission" }),
        it("x", { status: "missing" }),
    ];
    assert.deepEqual(needsFor(items, "resolve").map((h) => h.text), ["Needs Resolve: External scripting set to Local"]);
    assert.deepEqual(needsFor(items, "calibration").map((h) => h.id), ["scr"]);
    assert.deepEqual(needsFor(items, "finder-tags"), [], "unknown is not a reason to say Needs");
    assert.deepEqual(needsFor(items, "nope"), []);
    assert.deepEqual(needsFor(null, "resolve"), []);
});

test("fixKind maps the action id to what the button does", () => {
    assert.equal(fixKind({ fixAction: "open-accessibility-settings" }).pane, "accessibility");
    assert.equal(fixKind({ fixAction: "open-screen-recording-settings" }).pane, "screen-recording");
    assert.equal(fixKind({ fixAction: "open-automation-settings" }).pane, "automation");
    assert.equal(fixKind({ fixAction: "open-software-update" }).pane, "software-update");
    assert.deepEqual(fixKind({ fixAction: "copy-text", copyText: "brew install python" }), { type: "copy", text: "brew install python", label: "Copy the install line" });
    assert.equal(fixKind({ fixAction: "" }), null);
    assert.equal(fixKind({ fixAction: "format-the-disk" }), null);
    assert.equal(fixKind({ fixAction: "copy-text", copyText: "" }), null);
});
