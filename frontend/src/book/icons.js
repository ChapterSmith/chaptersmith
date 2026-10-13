// Static inline icons. Paths are constants; no caller data reaches innerHTML.
const paths = {
  folder: '<path d="M2.5 5.5a1.5 1.5 0 0 1 1.5-1.5h2.6l1.5 1.6H12a1.5 1.5 0 0 1 1.5 1.5v4.4A1.5 1.5 0 0 1 12 13H4a1.5 1.5 0 0 1-1.5-1.5z"/>',
  file: '<path d="M4.5 2.5h4.6l2.4 2.5v8a.5.5 0 0 1-.5.5h-6.5a.5.5 0 0 1-.5-.5v-10a.5.5 0 0 1 .5-.5z"/><path d="M6.4 10.8V7.6l3-.7v3.1"/><circle cx="5.7" cy="10.8" r=".8"/><circle cx="8.7" cy="10" r=".8"/>',
  projects: '<circle cx="8" cy="8" r="5.5"/><path d="M8 5v3.2l2 1.3"/>',
  open: '<path d="M2.5 12V5a1 1 0 0 1 1-1h2.6l1.5 1.6H12a1 1 0 0 1 1 1V8"/><path d="M2.5 12l1.6-4h10l-1.7 4z"/>',
  undo: '<path d="M5.5 3.5 2.8 6.2l2.7 2.7"/><path d="M3 6.2h6a3.6 3.6 0 0 1 0 7.2H6.5"/>',
  save: '<path d="M3 3.5h8l2 2v7a.5.5 0 0 1-.5.5h-9a.5.5 0 0 1-.5-.5z"/><path d="M5.5 3.5v3h4v-3M5.5 13v-3.5h5V13"/>',
  play: '<path d="M5 3.2v9.6l8-4.8z" fill="currentColor" stroke="none"/>',
  pause: '<path d="M4.5 3.2h2.4v9.6H4.5zM9.1 3.2h2.4v9.6H9.1z" fill="currentColor" stroke="none"/>',
  up: '<path d="M8 12.5v-9M4.5 7 8 3.5 11.5 7"/>',
  down: '<path d="M8 3.5v9M4.5 9 8 12.5 11.5 9"/>',
  grip: '<circle cx="6" cy="4" r="1" fill="currentColor" stroke="none"/><circle cx="10" cy="4" r="1" fill="currentColor" stroke="none"/><circle cx="6" cy="8" r="1" fill="currentColor" stroke="none"/><circle cx="10" cy="8" r="1" fill="currentColor" stroke="none"/><circle cx="6" cy="12" r="1" fill="currentColor" stroke="none"/><circle cx="10" cy="12" r="1" fill="currentColor" stroke="none"/>',
  adjust: '<path d="M2.5 5h4M9.5 5h4M2.5 11h2M7.5 11h6"/><circle cx="8" cy="5" r="1.5"/><circle cx="6" cy="11" r="1.5"/>',
  analyze: '<path d="M2.5 8h1.6l1.4-4 2.2 8 1.8-6 1.2 2h2.8"/>',
  export: '<path d="M8 2.5v7.5M4.8 7 8 10.2 11.2 7"/><path d="M3 12.8h10"/>',
  close: '<path d="M4 4l8 8M12 4l-8 8"/>',
  image: '<rect x="2.5" y="3" width="11" height="10" rx="1.5"/><circle cx="6" cy="6.5" r="1.1"/><path d="M3 11.5l3.2-3 2.3 2 1.8-1.5 2.7 2.5"/>',
  wrench: '<path d="M10.2 2.6a3.2 3.2 0 0 0-3.4 4.3l-4 4a1.3 1.3 0 0 0 1.9 1.9l4-4a3.2 3.2 0 0 0 4.2-3.5l-1.9 1.9-1.7-.4-.4-1.7z"/>',
  plus: '<path d="M8 3.5v9M3.5 8h9"/>',
  check: '<path d="M3.5 8.5l3 3 6-7"/>',
};

export function icon(document, name) {
  const node = document.createElement("span");
  node.className = "icon";
  node.setAttribute("aria-hidden", "true");
  node.innerHTML = `<svg viewBox="0 0 16 16" width="16" height="16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round">${paths[name] || ""}</svg>`;
  return node;
}
