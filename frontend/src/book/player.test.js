import test from "node:test";
import assert from "node:assert/strict";
import { createPlayer } from "./player.js";
import { createBookController } from "./controller.js";

class Audio extends EventTarget {
  readyState = 0;
  currentTime = 0;
  pauses = 0;
  src = "";
  pause() { this.pauses++; }
  load() {}
  removeAttribute() { this.src = ""; }
  play() { return Promise.reject(new Error("gesture required")); }
}
const tick = () => new Promise((resolve) => setImmediate(resolve));

test("player waits for metadata, reports rejected play and releases each owned preview", async () => {
  const audio = new Audio(), released = [], errors = [];
  const player = createPlayer({
    audio,
    releaseMedia: (id) => released.push(id),
    onError: (error) => errors.push(error),
  });
  player.load({ mediaId: "a", url: "/_media/a" });
  player.seek(8);
  assert.equal(audio.currentTime, 0);
  audio.readyState = 1;
  audio.dispatchEvent(new Event("loadedmetadata"));
  assert.equal(audio.currentTime, 8);
  await player.play();
  assert.match(errors[0], /gesture required/);
  player.load({ mediaId: "b", url: "/_media/b" });
  player.clear();
  player.dispose();
  assert.deepEqual(released, ["a", "b"]);
  assert.equal(audio.src, "");
  assert.ok(audio.pauses >= 2);
});

function delayedPreviewFixture(player) {
  let resolve, nextID = 0;
  const released = [];
  const view = { revision: 1, project: { id: "p" }, chapterRows: [] };
  const api = {
    onOperation: () => () => {},
    async call(method, ...args) {
      if (method === "OpenProject") return view;
      if (method === "PreviewChapter") {
        return new Promise((done) => {
          resolve = () => done({
            requestId: args[0], projectId: "p", revision: 1,
            mediaId: "late", url: "/_media/late",
          });
        });
      }
      if (method === "ReleaseMedia") released.push(args[0]);
      if (method === "ApplyEdit") return { ...view, revision: 2 };
    },
  };
  const controller = createBookController({ api, makeRequestId: () => String(++nextID) });
  controller.attachPlayer(player);
  return { controller, released, resolve: () => resolve() };
}

test("controller discards a late preview once after edit, clears playing audio", async () => {
  const loaded = [];
  let clears = 0;
  const { controller, released, resolve } = delayedPreviewFixture({
    clear: () => clears++, load: (preview) => loaded.push(preview), dispose() {},
  });
  await controller.open("p");
  const preview = controller.previewChapter("c");
  await tick();
  const edit = controller.edit({ kind: "cover-remove" });
  resolve();
  await preview;
  await edit;
  assert.deepEqual(released, ["late"]);
  assert.deepEqual(loaded, []);
  assert.ok(clears > 0);
  controller.dispose();
  assert.deepEqual(released, ["late"]);
});

test("disposed controller releases late preview exactly once without touching player", async () => {
  let disposed = 0;
  const { controller, released, resolve } = delayedPreviewFixture({
    clear() {},
    load() { assert.fail("loaded after dispose"); },
    dispose() { disposed++; },
  });
  await controller.open("p");
  const pending = controller.previewChapter("c");
  await tick();
  controller.dispose();
  resolve();
  await pending;
  controller.dispose();
  assert.equal(disposed, 1);
  assert.deepEqual(released, ["late"]);
});

test("late rejected play from cleared audio does not overwrite the next preview error", async () => {
  const audio = new Audio(), errors = [];
  let reject;
  audio.play = () => new Promise((_, fail) => { reject = fail; });
  const player = createPlayer({ audio, releaseMedia() {}, onError: (error) => errors.push(error) });
  player.load({ mediaId: "a", url: "/_media/a" });
  const playing = player.play();
  player.clear();
  reject(new Error("old rejection"));
  await playing;
  assert.deepEqual(errors, []);
  player.load({ mediaId: "b", url: "/_media/b" });
  audio.error = { code: 4, message: "codec unavailable" };
  audio.dispatchEvent(new Event("error"));
  assert.match(errors[0], /code 4.*codec unavailable/);
  player.dispose();
});

test('shared player releases repair with its owning callback and clear awaits cleanup', async () => {
  const audio = new Audio(), released = []; let finish;
  const player=createPlayer({audio,releaseMedia:id=>released.push(['book',id])});
  player.load({mediaId:'chapter',url:'/_media/chapter'});
  await player.clear();
  player.load({mediaId:'repair',url:'/_media/repair'},()=>new Promise(resolve=>{released.push(['repair','repair']);finish=resolve;}));
  let cleared=false; const clearing=player.clear().then(()=>cleared=true);
  await tick(); assert.equal(cleared,false); assert.equal(audio.src,'');
  finish(); await clearing; await player.clear();
  assert.deepEqual(released,[['book','chapter'],['repair','repair']]);
});
