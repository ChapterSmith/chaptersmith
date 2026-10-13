import { element } from './metadata.js';
import { displayTimeUs, formatTimeUs, parseTimeUs } from './state.js';
import { icon } from './icons.js';

const TIME_HINT = 'HH:MM:SS.ffffff';

// Only Go edits the timeline. All controls submit commands against derived chapter rows.
export function mountChapters(root, controller) {
  const document = root.ownerDocument;
  let currentView = null, currentSuggestions = null, currentSplits = null, externalDisabled = false, dragged = '', active = true;
  let constraints = [], suggestionConstraints = [], splitConstraints = [];
  // Where a single-chapter search shows its answer, by chapter id.
  const inlineHosts = new Map();
  // Rows are rebuilt after every edit; remember which editors the user opened.
  const openEditors = new Set();
  const error = element(document, 'p', '', 'book-error banner');
  error.setAttribute('role', 'alert');
  const tableHost = element(document, 'div', '', 'chapter-list');
  const options = element(document, 'section', '', 'boundary-suggestions');
  const proposalsHost = element(document, 'div', '', 'boundary-proposals');
  const button = (parent, text, action, handler, { variant = 'btn-ghost btn-sm', glyph = '' } = {}) => {
    const node = element(document, 'button', text, `btn ${variant}`);
    node.type = 'button'; node.dataset.action = action;
    if (glyph) node.prepend(icon(document, glyph));
    node.addEventListener('click', () => run(handler)); parent.append(node); return node;
  };
  const field = (parent, label, node) => {
    const wrapper = element(document, 'label', '', 'field');
    wrapper.append(element(document, 'span', label, 'field-label'), node);
    parent.append(wrapper); return node;
  };
  const input = (parent, label, value, name) => {
    const node = element(document, 'input', '', 'field-input');
    node.value = value; node.name = name;
    return field(parent, label, node);
  };
  const timeInput = (parent, label, value, name) => {
    const node = input(parent, label, value, name);
    node.placeholder = TIME_HINT; node.title = TIME_HINT; node.classList.add('mono');
    return node;
  };
  const select = (parent, label, values, name) => {
    const node = element(document, 'select', '', 'field-input'); node.name = name;
    for (const [value, text] of values) { const option = element(document, 'option', text); option.value = value; node.append(option); }
    return field(parent, label, node);
  };
  async function run(action) {
    if (externalDisabled || controller.getState().operation) return;
    error.textContent = '';
    try { await action(); } catch (err) { if (active) error.textContent = err?.message || String(err); }
  }

  const optionsHead = element(document, 'div', '', 'tool-head');
  optionsHead.append(
    element(document, 'h3', 'Snap chapter starts to silence'),
    element(document, 'p', 'Looks for silence near each chapter start and suggests a cleaner cut. Nothing changes until you accept a suggestion, and export loudness normalization is unaffected.'),
  );
  const optionFields = element(document, 'div', '', 'tool-fields');
  const windowInput = timeInput(optionFields, 'Search window ±', '00:00:05.000000', 'window');
  const threshold = input(optionFields, 'Silence threshold (dBFS)', '-40', 'threshold');
  threshold.type = 'number'; threshold.max = '0'; threshold.step = 'any';
  const minimum = timeInput(optionFields, 'Minimum silence', '00:00:00.500000', 'minimum');
  // One set of search settings serves the whole book and single chapters.
  const silenceOptions = () => {
    const windowUs = parseTimeUs(windowInput.value), minSilenceUs = parseTimeUs(minimum.value), thresholdDb = Number(threshold.value);
    if (!windowUs || !minSilenceUs || threshold.value.trim() === '' || !Number.isFinite(thresholdDb) || thresholdDb > 0) throw Error('Enter positive times and a threshold of 0 dBFS or lower');
    return { windowUs, thresholdDb, minSilenceUs };
  };
  button(optionFields, 'Find suggestions', 'suggest', () => controller.suggestBoundaries(silenceOptions()), { variant: 'btn-accent' });
  options.append(optionsHead, optionFields, proposalsHost);

  const splitTool = element(document, 'section', '', 'boundary-suggestions');
  const splitHead = element(document, 'div', '', 'tool-head');
  splitHead.append(
    element(document, 'h3', 'Create chapters from pauses'),
    element(document, 'p', 'Listens to the whole book and proposes a new chapter in the middle of each long pause. Use it for a recording that has no chapters yet. Nothing changes until you choose which breaks to keep.'),
  );
  const splitFields = element(document, 'div', '', 'tool-fields');
  const pauseInput = timeInput(splitFields, 'Shortest pause that counts', '00:00:02.000000', 'split-pause');
  const splitThreshold = input(splitFields, 'Silence threshold (dBFS)', '-40', 'split-threshold');
  splitThreshold.type = 'number'; splitThreshold.max = '0'; splitThreshold.step = 'any';
  const lengthInput = timeInput(splitFields, 'Shortest chapter', '00:05:00.000000', 'split-length');
  button(splitFields, 'Find chapter breaks', 'suggest-splits', () => {
    const minSilenceUs = parseTimeUs(pauseInput.value), minChapterUs = parseTimeUs(lengthInput.value), thresholdDb = Number(splitThreshold.value);
    if (!minSilenceUs || splitThreshold.value.trim() === '' || !Number.isFinite(thresholdDb) || thresholdDb > 0) throw Error('Enter a positive pause length and a threshold of 0 dBFS or lower');
    return controller.suggestSplits({ minSilenceUs, thresholdDb, minChapterUs });
  }, { variant: 'btn-accent' });
  const splitsHost = element(document, 'div', '', 'boundary-proposals');
  splitTool.append(splitHead, splitFields, splitsHost);
  const pauseTool = element(document, 'section', '', 'boundary-suggestions');
  const pauseHead = element(document, 'div', '', 'tool-head');
  pauseHead.append(
    element(document, 'h3', 'Pause between chapters'),
    element(document, 'p', 'Gives every chapter the same pause before it. Replacing first cuts the silence the recording already has at each chapter start (up to 5 s either side, below −40 dBFS), so the pause ends up exactly this long. One Undo reverses it.'),
  );
  const pauseFields = element(document, 'div', '', 'tool-fields');
  const pauseLength = timeInput(pauseFields, 'Pause length', '00:00:02.000000', 'pause-length');
  const replaceRow = element(document, 'label', '', 'check-row');
  const replace = element(document, 'input'); replace.type = 'checkbox'; replace.name = 'pause-replace'; replace.checked = true;
  replaceRow.append(replace, element(document, 'span', 'Replace the silence that is already there', 'check-title'));
  pauseFields.append(replaceRow);
  const pauseStatus = element(document, 'p', '', 'muted'); pauseStatus.setAttribute('role', 'status');
  const setPauses = button(pauseFields, 'Set pauses', 'set-pauses', async () => {
    const durationUs = parseTimeUs(pauseLength.value);
    pauseStatus.textContent = '';
    const pauses = replace.checked ? (await controller.measurePauses({ thresholdDb: -40, windowUs: 5000000 })).pauses : [];
    if (!replace.checked && !durationUs) throw Error('Enter a pause longer than zero, or replace the existing silence to close the gaps');
    const view = await controller.edit({ kind: 'pauses', pauses, durationUs });
    const starts = (view?.chapterRows?.length || 1) - 1, replaced = pauses.filter(pause => pause.beforeUs || pause.afterUs).length;
    pauseStatus.textContent = `Pause set at ${starts} chapter ${starts === 1 ? 'start' : 'starts'}${replace.checked ? `; existing silence replaced at ${replaced}` : ''}.`;
  }, { variant: 'btn-accent' });
  pauseTool.append(pauseHead, pauseFields, pauseStatus);
  root.append(error, tableHost, splitTool, options, pauseTool);

  function renderRows(view) {
    constraints = []; tableHost.replaceChildren(); inlineHosts.clear();
    const rows = view?.chapterRows || [];
    for (const id of [...openEditors]) if (!rows.some(row => row.id === id)) openEditors.delete(id);
    const head = element(document, 'div', '', 'chapter-head');
    for (const label of ['', '#', 'Title', 'Start', 'Duration', '']) head.append(element(document, 'span', label));
    tableHost.append(head);
    if (!rows.length) tableHost.append(element(document, 'p', 'This book has no chapters.', 'muted'));
    for (const [index, row] of rows.entries()) {
      // Draggable only while the grip is held, so titles stay selectable.
      const item = element(document, 'div', '', 'chapter'); item.dataset.chapterId = row.id; item.draggable = false;
      const main = element(document, 'div', '', 'chapter-main');
      const grip = element(document, 'span', '', 'chapter-grip'); grip.append(icon(document, 'grip')); grip.title = 'Drag to reorder';
      grip.addEventListener('pointerdown', () => { if (!disabled()) item.draggable = true; });
      const release = () => { item.draggable = false; item.classList.remove('drop-target'); };
      grip.addEventListener('pointerup', release); item.addEventListener('dragend', release);
      const titleCell = element(document, 'div', '', 'chapter-title');
      const title = element(document, 'input', '', 'field-input chapter-title-input');
      title.name = 'title'; title.value = row.title; title.setAttribute('aria-label', `Title of chapter ${index + 1}`);
      const rename = button(titleCell, 'Rename', 'rename', () => controller.edit({ kind: 'rename', chapterId: row.id, title: title.value }), { variant: 'btn-accent btn-sm' });
      titleCell.prepend(title);
      const unchanged = () => title.value === row.title;
      const syncRename = () => { rename.hidden = unchanged(); };
      title.addEventListener('input', () => { syncRename(); refreshDisabled(); });
      title.addEventListener('keydown', event => {
        if (event.key === 'Enter' && !unchanged()) { event.preventDefault(); rename.click(); }
        if (event.key === 'Escape') { title.value = row.title; syncRename(); }
      });
      syncRename();
      constraints.push([rename, unchanged]);
      const actions = element(document, 'div', '', 'chapter-actions');
      const move = destination => controller.edit({ kind: 'move', chapterId: row.id, destination });
      const up = button(actions, '', 'up', () => move(index - 1), { variant: 'btn-icon', glyph: 'up' });
      const down = button(actions, '', 'down', () => move(index + 1), { variant: 'btn-icon', glyph: 'down' });
      up.setAttribute('aria-label', `Move ${row.title} up`); down.setAttribute('aria-label', `Move ${row.title} down`);
      up.title = 'Move up'; down.title = 'Move down';
      constraints.push([up, () => index === 0], [down, () => index === rows.length - 1]);
      item.addEventListener('dragstart', event => { if (disabled()) { event.preventDefault(); return; } dragged = row.id; event.dataTransfer?.setData('text/plain', row.id); });
      item.addEventListener('dragover', event => { if (!disabled()) { event.preventDefault(); item.classList.add('drop-target'); } });
      item.addEventListener('dragleave', () => item.classList.remove('drop-target'));
      item.addEventListener('drop', event => { event.preventDefault(); item.classList.remove('drop-target'); const id = event.dataTransfer?.getData('text/plain') || dragged; if (rows.some(r => r.id === id)) run(() => controller.edit({ kind: 'move', chapterId: id, destination: index })); dragged = ''; });
      const preview = button(actions, 'Preview', 'preview-chapter', () => controller.previewChapter(row.id), { glyph: 'play' }); preview.dataset.chapterId = row.id;
      const editor = element(document, 'div', '', 'chapter-editor');
      editor.id = `chapter-editor-${index}`;
      const adjust = element(document, 'button', 'Adjust', 'btn btn-ghost btn-sm');
      adjust.type = 'button'; adjust.dataset.action = 'adjust'; adjust.prepend(icon(document, 'adjust'));
      adjust.setAttribute('aria-controls', editor.id);
      const showEditor = open => { editor.hidden = !open; adjust.setAttribute('aria-expanded', String(open)); item.classList.toggle('is-open', open); };
      adjust.addEventListener('click', () => { if (openEditors.has(row.id)) openEditors.delete(row.id); else openEditors.add(row.id); showEditor(openEditors.has(row.id)); });
      actions.append(adjust);
      showEditor(openEditors.has(row.id));
      main.append(grip, element(document, 'span', String(index + 1), 'chapter-index'), titleCell,
        element(document, 'span', displayTimeUs(row.startUs), 'chapter-time'), element(document, 'span', displayTimeUs(row.durationUs), 'chapter-time'), actions);

      const boundaryGroup = element(document, 'div', '', 'editor-group');
      boundaryGroup.append(element(document, 'h4', 'Move chapter start'));
      if (!index) boundaryGroup.append(element(document, 'p', 'The first chapter always starts at the beginning of the book.', 'muted'));
      const boundaryFields = element(document, 'div', '', 'tool-fields'); boundaryGroup.append(boundaryFields);
      const start = timeInput(boundaryFields, 'New start in the book', formatTimeUs(row.startUs), 'boundary-start');
      const mode = select(boundaryFields, 'Audio between old and new start', [['transfer', 'Give to / take from previous chapter'], ['remove', 'Delete from this chapter']], 'boundary-mode');
      const boundary = button(boundaryFields, 'Apply new start', 'boundary', () => controller.edit({ kind: 'boundary', chapterId: row.id, newStartUs: parseTimeUs(start.value), mode: mode.value }), { variant: 'btn-accent btn-sm' });
      const invalidBoundary = () => {
        if (!index) return true;
        try { const value = parseTimeUs(start.value); return value <= rows[index - 1].startUs || value >= row.startUs + row.durationUs || (mode.value === 'remove' && value <= row.startUs); } catch { return true; }
      };
      const nearby = button(boundaryFields, 'Find silence near this start', 'suggest-chapter', () => controller.suggestBoundaries({ ...silenceOptions(), chapterId: row.id }), { glyph: 'analyze' });
      nearby.title = 'Uses the search settings below the chapter list';
      const inline = element(document, 'div', '', 'chapter-suggestion'); boundaryGroup.append(inline); inlineHosts.set(row.id, inline);
      constraints.push([start, () => !index], [mode, () => !index], [boundary, invalidBoundary], [nearby, () => !index]);
      start.addEventListener('input', refreshDisabled); mode.addEventListener('change', refreshDisabled);
      const silenceGroup = element(document, 'div', '', 'editor-group');
      silenceGroup.append(element(document, 'h4', 'Insert silence'));
      const silenceFields = element(document, 'div', '', 'tool-fields'); silenceGroup.append(silenceFields);
      const duration = timeInput(silenceFields, 'Length', '00:00:00.500000', 'silence-duration');
      const position = select(silenceFields, 'Where', [['start', 'At the start'], ['end', 'At the end']], 'silence-position');
      const silence = button(silenceFields, 'Add silence', 'silence', () => controller.edit({ kind: 'silence', chapterId: row.id, durationUs: parseTimeUs(duration.value), position: position.value }), { variant: 'btn-accent btn-sm' });
      constraints.push([silence, () => { try { return parseTimeUs(duration.value) <= 0; } catch { return true; } }]); duration.addEventListener('input', refreshDisabled);
      const splitGroup = element(document, 'div', '', 'editor-group wide');
      splitGroup.append(element(document, 'h4', 'Split or merge'));
      const splitRow = element(document, 'div', '', 'tool-fields'); splitGroup.append(splitRow);
      const splitAt = timeInput(splitRow, 'Split at this time in the book', formatTimeUs(row.startUs + Math.floor(row.durationUs / 2)), 'split-at');
      const split = button(splitRow, 'Split here', 'split', () => controller.edit({ kind: 'split', positionsUs: [parseTimeUs(splitAt.value)] }), { variant: 'btn-accent btn-sm' });
      const merge = button(splitRow, 'Merge with previous chapter', 'merge', () => controller.edit({ kind: 'merge', chapterId: row.id }));
      constraints.push([split, () => { try { const at = parseTimeUs(splitAt.value); return at <= row.startUs || at >= row.startUs + row.durationUs; } catch { return true; } }], [merge, () => !index]);
      splitAt.addEventListener('input', refreshDisabled);
      editor.append(boundaryGroup, silenceGroup, splitGroup);
      item.append(main, editor); tableHost.append(item);
    }
  }
  const describe = proposal => {
    const delta = proposal.newStartUs - proposal.oldStartUs;
    return `${formatTimeUs(proposal.oldStartUs)} → ${formatTimeUs(proposal.newStartUs)} (${delta < 0 ? '−' : '+'}${formatTimeUs(Math.abs(delta))})`;
  };
  function renderSuggestions(suggestions) {
    proposalsHost.replaceChildren(); suggestionConstraints = [];
    for (const host of inlineHosts.values()) host.replaceChildren();
    if (!suggestions) return;
    const proposals = suggestions.proposals || [];
    if (suggestions.scope) {
      // A single-chapter search answers inside that chapter's editor.
      const host = inlineHosts.get(suggestions.scope);
      if (!host) return;
      if (!proposals.length) { host.append(element(document, 'p', 'No silence found near this start with the current search settings.', 'muted')); return; }
      const proposal = proposals[0];
      host.append(element(document, 'p', `Suggested start: ${describe(proposal)}`));
      const row = element(document, 'div', '', 'action-row'); host.append(row);
      button(row, 'Listen', 'preview-boundary', () => controller.previewBoundary(proposal), { glyph: 'play' });
      button(row, 'Use this start', 'accept-boundaries', () => controller.applyBoundaries([proposal]), { variant: 'btn-accent btn-sm' });
      button(row, 'Dismiss', 'reject-boundaries', () => controller.rejectSuggestions());
      return;
    }
    if (!proposals.length) { proposalsHost.append(element(document, 'p', 'No suggestions — the book was not changed.', 'muted')); return; }
    const selected = new Set();
    for (const proposal of proposals) {
      const row = element(document, 'div', '', 'boundary-proposal');
      const label = element(document, 'label');
      const check = element(document, 'input'); check.type = 'checkbox'; check.checked = false;
      const title = currentView.chapterRows.find(r => r.id === proposal.chapterId)?.title || proposal.chapterId;
      label.append(check, element(document, 'span', `${title}: ${describe(proposal)}`));
      check.addEventListener('change', () => { if (check.checked) selected.add(proposal); else selected.delete(proposal); refreshDisabled(); });
      row.append(label); button(row, 'Listen', 'preview-boundary', () => controller.previewBoundary(proposal), { glyph: 'play' }); proposalsHost.append(row);
    }
    const footer = element(document, 'div', '', 'action-row');
    const accept = button(footer, 'Apply selected', 'accept-boundaries', () => controller.applyBoundaries([...selected]), { variant: 'btn-accent btn-sm' });
    suggestionConstraints.push([accept, () => selected.size === 0]);
    button(footer, 'Dismiss', 'reject-boundaries', () => controller.rejectSuggestions());
    proposalsHost.append(footer);
  }
  function renderSplits(splits) {
    splitsHost.replaceChildren(); splitConstraints = [];
    if (!splits) return;
    const proposals = splits.proposals || [];
    if (!proposals.length) { splitsHost.append(element(document, 'p', 'No pauses of that length were found — the book was not changed.', 'muted')); return; }
    // Every break starts selected: the list is reviewed by removing, not adding.
    const selected = new Set(proposals), checks = [];
    const footer = element(document, 'div', '', 'action-row');
    const accept = button(footer, '', 'accept-splits', () => controller.edit({ kind: 'split', positionsUs: [...selected].map(p => p.atUs) }), { variant: 'btn-accent btn-sm' });
    const count = () => { accept.textContent = `Create ${selected.size} ${selected.size === 1 ? 'chapter' : 'chapters'}`; };
    for (const proposal of proposals) {
      const row = element(document, 'div', '', 'boundary-proposal');
      const label = element(document, 'label');
      const check = element(document, 'input'); check.type = 'checkbox'; check.checked = true; checks.push([check, proposal]);
      label.append(check, element(document, 'span', `New chapter at ${displayTimeUs(proposal.atUs)} · ${(proposal.silenceUs / 1000000).toFixed(1)} s pause`));
      check.addEventListener('change', () => { if (check.checked) selected.add(proposal); else selected.delete(proposal); count(); refreshDisabled(); });
      row.append(label); button(row, 'Listen', 'preview-split', () => controller.previewPosition(proposal.atUs), { glyph: 'play' }); splitsHost.append(row);
    }
    const toggle = element(document, 'button', 'Select none', 'btn btn-ghost btn-sm'); toggle.type = 'button'; toggle.dataset.action = 'toggle-splits';
    toggle.addEventListener('click', () => {
      const all = selected.size < proposals.length;
      for (const [check, proposal] of checks) { check.checked = all; if (all) selected.add(proposal); else selected.delete(proposal); }
      toggle.textContent = all ? 'Select none' : 'Select all'; count(); refreshDisabled();
    });
    footer.append(toggle);
    button(footer, 'Dismiss', 'reject-splits', () => controller.rejectSuggestions());
    splitConstraints.push([accept, () => selected.size === 0]);
    count(); splitsHost.append(footer);
  }
  function disabled() { return externalDisabled || !!controller.getState().operation || !controller.getState().view; }
  function refreshDisabled() {
    const busy = disabled();
    for (const node of root.querySelectorAll('button,input,select')) node.disabled = busy;
    for (const [node, invalid] of [...constraints, ...suggestionConstraints, ...splitConstraints]) node.disabled = busy || invalid();
    setPauses.disabled = busy || (currentView?.chapterRows?.length || 0) < 2;
    if (controller.getState().exportOperation) for (const node of root.querySelectorAll('[data-action="preview-chapter"],[data-action="preview-boundary"],[data-action="preview-split"],[data-action="suggest"],[data-action="suggest-chapter"],[data-action="suggest-splits"]')) node.disabled = true;
    if (busy) for (const item of tableHost.querySelectorAll('.chapter')) item.draggable = false;
  }
  const unsubscribe = controller.subscribe(state => {
    if (state.view !== currentView) { currentView = state.view; renderRows(currentView); }
    if (state.suggestions !== currentSuggestions) { currentSuggestions = state.suggestions; renderSuggestions(currentSuggestions); }
    if (state.splits !== currentSplits) { currentSplits = state.splits; renderSplits(currentSplits); }
    refreshDisabled();
  });
  const dispose = () => { active = false; unsubscribe(); };
  dispose.setDisabled = value => { externalDisabled = value; refreshDisabled(); };
  return dispose;
}
