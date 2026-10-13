const SERVICE = "chaptersmith/internal/gui.BookService";
const payload = (event) =>
  Array.isArray(event?.data) ? event.data[0] : (event?.data ?? event);

export function createBookAPI(call, events) {
  const invoke = typeof call === "function" ? call : call.ByName.bind(call);
  const subscribe = (name, handler) => {
    const cleanup = events.On(name, (event) => handler(payload(event)));
    return () => cleanup();
  };
  return {
    call: (method, ...args) => invoke(`${SERVICE}.${method}`, ...args),
    onOperation: (handler) => subscribe("book-operation", handler),
    onLog: (handler) => subscribe("book-log", handler),
  };
}
