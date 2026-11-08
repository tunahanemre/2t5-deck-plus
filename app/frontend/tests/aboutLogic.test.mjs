import test from "node:test";
import assert from "node:assert/strict";
import { DEFAULT_LINKS, aboutModel, interceptClick, openAction } from "../public/editor/aboutLogic.js";

test("the three links are exactly the company ones, https only", () => {
    assert.deepEqual(DEFAULT_LINKS.map((l) => l.id), ["site", "instagram", "sponsors"]);
    assert.deepEqual(DEFAULT_LINKS.map((l) => l.url), ["https://2t5.co", "https://instagram.com/tunahanemre", "https://github.com/sponsors/tunahanemre"]);
    assert.deepEqual(DEFAULT_LINKS.map((l) => l.label), ["2t5.co", "instagram.com/tunahanemre", "Donate"]);
});

test("aboutModel: falls back to the built-in text when the host has no /api/about", () => {
    const m = aboutModel(null);
    assert.equal(m.name, "2T5 DECK+");
    assert.equal(m.by, "2T5 Film");
    assert.equal(m.versionText, "Version unknown");
    assert.equal(m.canOpen, false);
    assert.equal(m.links.length, 3);
});

test("aboutModel: takes the version and links from the host, ignores unsafe ones", () => {
    const m = aboutModel({ name: "2T5 DECK+", by: "2T5 Film", version: "1.4.0", canOpen: true, links: [
        { id: "site", label: "2t5.co", url: "https://2t5.co" },
        { id: "evil", label: "x", url: "javascript:alert(1)" },
        { id: "file", label: "f", url: "file:///etc/passwd" },
        { id: "", label: "no id", url: "https://example.com" },
    ] });
    assert.equal(m.versionText, "Version 1.4.0");
    assert.equal(m.canOpen, true);
    assert.deepEqual(m.links.map((l) => l.id), ["site"]);
});

test("aboutModel: an empty or odd version never prints 'undefined'", () => {
    for (const v of ["", null, undefined, 3, "  "]) assert.equal(aboutModel({ version: v }).versionText, "Version unknown");
    assert.equal(aboutModel({ version: "dev" }).versionText, "Version dev");
});

test("aboutModel: a host reply with no usable links keeps the three built-in ones", () => {
    assert.equal(aboutModel({ version: "1", links: [] }).links.length, 3);
    assert.equal(aboutModel({ version: "1", links: "nope" }).links.length, 3);
});

test("interceptClick: only a plain primary click, only when the app can open links", () => {
    const plain = { button: 0, metaKey: false, ctrlKey: false, shiftKey: false, altKey: false, defaultPrevented: false };
    assert.equal(interceptClick(plain, true), true);
    assert.equal(interceptClick(plain, false), false, "dev server without opener: let the anchor work");
    assert.equal(interceptClick({ ...plain, metaKey: true }, true), false);
    assert.equal(interceptClick({ ...plain, ctrlKey: true }, true), false);
    assert.equal(interceptClick({ ...plain, shiftKey: true }, true), false);
    assert.equal(interceptClick({ ...plain, button: 1 }, true), false);
    assert.equal(interceptClick({ ...plain, defaultPrevented: true }, true), false);
});

test("openAction: ask the app first; if that fails fall back to a new tab", async () => {
    const calls = [];
    const ok = await openAction({ id: "site", url: "https://2t5.co" }, { open: async (id) => { calls.push(["app", id]); }, win: (u) => { calls.push(["win", u]); } });
    assert.equal(ok, "app");
    const bad = await openAction({ id: "site", url: "https://2t5.co" }, { open: async () => { throw new Error("no"); }, win: (u) => { calls.push(["win", u]); } });
    assert.equal(bad, "window");
    assert.deepEqual(calls, [["app", "site"], ["win", "https://2t5.co"]]);
});
