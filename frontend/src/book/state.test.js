import test from "node:test";
import assert from "node:assert/strict";
import { initialState, reduce, parseTimeUs, formatTimeUs } from "./state.js";

test("time parsing is exact and rejects malformed or unsafe values", () => {
  assert.equal(parseTimeUs("01:02:03.000004"), 3723000004);
  assert.equal(formatTimeUs(3723000004), "01:02:03.000004");
  assert.equal(formatTimeUs(0), "00:00:00.000000");
  for (const bad of [
    "-1",
    "1.2",
    "01:60:00",
    "01:00:60",
    "00:00:00.0000001",
    "2501999:47:34.740992",
    "1e6",
    "",
  ]) {
    assert.throws(() => parseTimeUs(bad));
  }
  for (const bad of [-1, 0.5, Number.MAX_SAFE_INTEGER + 1, NaN])
    assert.throws(() => formatTimeUs(bad));
  assert.equal(
    parseTimeUs(formatTimeUs(Number.MAX_SAFE_INTEGER)),
    Number.MAX_SAFE_INTEGER,
  );
});

test("stale result retains current view", () => {
  const view = { revision: 8, project: { id: "b" } };
  let state = reduce(initialState(), { type: "view", view });
  state = reduce(state, {
    type: "operation-start",
    requestId: "new",
    kind: "load",
  });
  assert.equal(
    reduce(state, {
      type: "operation-result",
      requestId: "old",
      revision: 8,
      result: { revision: 9 },
    }),
    state,
  );
  assert.equal(
    reduce(state, {
      type: "operation-status",
      status: { requestId: "old", status: "failed" },
    }),
    state,
  );
  assert.equal(
    reduce(state, { type: "operation-error", requestId: "old", error: "old" }),
    state,
  );
  assert.equal(reduce(state, { type: "unknown" }), state);
  assert.equal(reduce(state, { type: "view", view: { revision: 7 } }), state);
  assert.equal(
    reduce(state, {
      type: "operation-result",
      requestId: "new",
      revision: 7,
      result: { revision: 9 },
    }),
    state,
  );
});

test("success event before load reply keeps request identity until result commits", () => {
  let state = reduce(initialState(), {
    type: "operation-start",
    requestId: "load",
    kind: "load",
  });
  state = reduce(state, {
    type: "operation-status",
    status: {
      requestId: "load",
      kind: "load",
      projectId: "a",
      revision: 1,
      status: "succeeded",
    },
  });
  const view = { revision: 1, project: { id: "a" } };
  state = reduce(state, {
    type: "operation-result",
    requestId: "load",
    revision: null,
    result: view,
  });
  assert.equal(state.view, view);
  assert.equal(state.operation, null);
});

test("terminal failure unlocks controls and retains view", () => {
  const view = { revision: 3, project: { id: "a" } };
  let state = reduce(initialState(), { type: "view", view });
  state = reduce(state, {
    type: "operation-start",
    requestId: "x",
    kind: "load",
  });
  state = reduce(state, {
    type: "operation-status",
    status: { requestId: "x", status: "failed", error: "bad input" },
  });
  assert.equal(state.operation, null);
  assert.equal(state.view, view);
  assert.equal(state.error, "bad input");
});

test('analysis state updates preserve active export and edited book', () => {
  const before={...initialState(), view:{revision:4,project:{id:'p'}},exportOperation:{requestId:'e',kind:'export'}};
  const analysis={origin:'book',status:'analyzing'};
  const next=reduce(before,{type:'analysis-state',analysis});
  assert.equal(next.analysis,analysis); assert.equal(next.view,before.view);assert.equal(next.exportOperation,before.exportOperation);
});
