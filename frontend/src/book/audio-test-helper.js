// linkedom intentionally omits the native HTMLMediaElement implementation.
export function installAudioStub(document) {
  const createElement = document.createElement.bind(document);
  document.createElement = (tag, ...args) => {
    const node = createElement(tag, ...args);
    if (tag === "audio") {
      node.pause = () => {};
      node.load = () => {};
      node.play = async () => {};
    }
    return node;
  };
}
