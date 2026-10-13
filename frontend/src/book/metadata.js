export const metadataFields = [
  "title",
  "artist",
  "album",
  "albumArtist",
  "composer",
  "genre",
  "date",
  "copyright",
  "comment",
  "description",
  "subtitle",
  "grouping",
];

export function metadataSnapshot(metadata, values = {}) {
  const snapshot = {};
  for (const field of metadataFields)
    snapshot[field] = Object.hasOwn(values, field)
      ? values[field]
      : (metadata[field] ?? "");
  snapshot.extra = structuredClone(
    Object.hasOwn(values, "extra") ? values.extra : (metadata.extra ?? {}),
  );
  return snapshot;
}

export function element(document, tag, text = "", className = "") {
  const node = document.createElement(tag);
  if (text) node.textContent = text;
  if (className) node.className = className;
  return node;
}

export function mountMetadata(root, controller) {
  const document = root.ownerDocument;
  const form = element(document, "form");
  const fieldset = element(document, "fieldset", "", "metadata-fields");
  const basicGrid = element(document, "div", "", "field-grid");
  const advanced = element(document, "details", "", "more-metadata");
  advanced.append(element(document, "summary", "More metadata"));
  const advancedGrid = element(document, "div", "", "field-grid");
  advanced.append(advancedGrid);
  const extraRows = element(document, "div", "", "extra-rows");
  const localError = element(document, "p", "", "book-error");
  localError.setAttribute("role", "alert");
  let currentView = null,
    touched = new Set(),
    extrasTouched = false,
    active = true;
  const fields = new Map();
  const labels = {
    title: "Title",
    artist: "Author",
    composer: "Narrator",
    album: "Album",
    genre: "Genre",
    date: "Date",
    description: "Description",
    albumArtist: "Album artist",
    copyright: "Copyright",
    comment: "Comment",
    subtitle: "Subtitle",
    grouping: "Grouping",
  };
  const basic = new Set([
    "title",
    "artist",
    "composer",
    "album",
    "genre",
    "date",
    "description",
  ]);
  function notify() {
    root.dispatchEvent(
      new document.defaultView.Event("book-draft-change", { bubbles: true }),
    );
  }
  for (const name of metadataFields) {
    const row = element(document, "label", "", "field");
    row.dataset.field = name;
    row.append(element(document, "span", labels[name], "field-label"));
    const input = element(
      document,
      name === "description" || name === "comment" ? "textarea" : "input",
      "",
      "field-input",
    );
    input.name = name;
    if (input.tagName === "TEXTAREA")
      input.rows = name === "description" ? 4 : 2;
    input.addEventListener("input", () => {
      touched.add(name);
      notify();
    });
    fields.set(name, input);
    row.append(input);
    (basic.has(name) ? basicGrid : advancedGrid).append(row);
  }
  advanced.append(
    element(document, "h3", "Extra tags", "card-title"),
    extraRows,
  );
  function extraRow(key = "", value = "") {
    const row = element(document, "div", "", "field-row extra-row");
    const keyInput = element(document, "input", "", "field-input");
    keyInput.setAttribute("aria-label", "Extra tag key");
    keyInput.placeholder = "Tag";
    keyInput.dataset.extraKey = "";
    keyInput.value = key;
    const valueInput = element(document, "input", "", "field-input");
    valueInput.setAttribute("aria-label", "Extra tag value");
    valueInput.placeholder = "Value";
    valueInput.dataset.extraValue = "";
    valueInput.value = value;
    const remove = element(
      document,
      "button",
      "Remove",
      "btn btn-ghost btn-sm",
    );
    remove.type = "button";
    remove.dataset.removeExtra = key;
    remove.addEventListener("click", () => {
      row.remove();
      extrasTouched = true;
      notify();
    });
    for (const input of [keyInput, valueInput])
      input.addEventListener("input", () => {
        extrasTouched = true;
        notify();
      });
    row.append(keyInput, valueInput, remove);
    extraRows.append(row);
  }
  const add = element(document, "button", "Add tag", "btn btn-ghost btn-sm");
  add.dataset.action = "add-extra";
  add.type = "button";
  add.addEventListener("click", () => {
    extraRow();
    extrasTouched = true;
    notify();
    extraRows.lastChild.firstChild.focus();
  });
  advanced.append(add);
  fieldset.append(basicGrid, advanced);
  const actions = element(document, "div", "", "action-row form-actions");
  const apply = element(document, "button", "Apply changes", "btn btn-accent");
  apply.type = "submit";
  const discard = element(
    document,
    "button",
    "Discard",
    "btn btn-ghost",
  );
  discard.type = "button";
  actions.append(apply, discard);
  fieldset.append(actions);
  form.append(fieldset, localError);
  root.append(form);
  function render(view, reset = false) {
    if (!view) return;
    const switched = currentView?.project.id !== view.project.id;
    if (reset || switched) {
      touched.clear();
      extrasTouched = false;
    }
    for (const [name, input] of fields)
      if (reset || switched || !touched.has(name))
        input.value = view.project.metadata[name] ?? "";
    if (reset || switched || !extrasTouched) {
      extraRows.replaceChildren();
      for (const [key, value] of Object.entries(
        view.project.metadata.extra ?? {},
      ))
        extraRow(key, value);
    }
    currentView = view;
  }
  function snapshot() {
    const values = {};
    for (const [name, input] of fields) values[name] = input.value;
    const extra = Object.create(null);
    for (const row of extraRows.children) {
      const key = row.querySelector("[data-extra-key]").value;
      if (Object.hasOwn(extra, key))
        throw new Error(`Duplicate Extra tag: ${key}`);
      extra[key] = row.querySelector("[data-extra-value]").value;
    }
    values.extra = extra;
    return metadataSnapshot(currentView.project.metadata, values);
  }
  function isDirty() {
    if (!currentView) return false;
    try {
      return (
        JSON.stringify(snapshot()) !==
        JSON.stringify(metadataSnapshot(currentView.project.metadata))
      );
    } catch {
      return true;
    }
  }
  async function submit() {
    if (!currentView || !isDirty()) return currentView;
    localError.textContent = "";
    try {
      const result = await controller.edit({
        kind: "metadata",
        metadata: snapshot(),
      });
      if (
        active &&
        result?.project.id === controller.getState().view?.project.id
      ) {
        render(controller.getState().view, true);
        notify();
      }
      return result;
    } catch (error) {
      if (active) localError.textContent = error?.message || String(error);
      throw error;
    }
  }
  form.addEventListener("submit", (event) => {
    event.preventDefault();
    submit().catch(() => {});
  });
  discard.addEventListener("click", () => {
    render(controller.getState().view, true);
    localError.textContent = "";
    notify();
  });
  const unsubscribe = controller.subscribe((state) => {
    if (state.view !== currentView) render(state.view);
    const disabled = !!state.operation || !state.view;
    fieldset.disabled = disabled;
    for (const input of form.querySelectorAll("input,textarea,button"))
      input.disabled = disabled;
  });
  const cleanup = () => {
    active = false;
    unsubscribe();
    form.remove();
  };
  cleanup.isDirty = isDirty;
  cleanup.submit = submit;
  cleanup.discard = () => {
    render(controller.getState().view, true);
    localError.textContent = "";
    notify();
  };
  return cleanup;
}
