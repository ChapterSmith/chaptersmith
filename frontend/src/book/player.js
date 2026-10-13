// Owns one media token. The controller retains ownership until load accepts it.
export function createPlayer({ audio, releaseMedia, onError = () => {} }) {
  let current = null, pendingSeek = null, disposed = false, generation = 0;
  const waiters = new Set();
  const settle = () => { for (const done of [...waiters]) done(); };
  const report = (error) => onError(error?.message || String(error));
  function clear() {
    generation++;
    const prior = current;
    current = null;
    pendingSeek = null;
    settle();
    audio.pause();
    audio.removeAttribute("src");
    audio.load();
    let released;
    try { released = Promise.resolve(prior?.release(prior.preview.mediaId)); }
    catch (error) { released = Promise.reject(error); }
    // Existing chapter callers may clear synchronously; analysis also awaits this
    // same promise before handing control to repair encoding.
    released.catch(report);
    return released;
  }
  function seek(seconds) {
    if (!current || !Number.isFinite(seconds) || seconds < 0) return;
    if (audio.readyState < 1) pendingSeek = seconds;
    else { audio.currentTime = seconds; pendingSeek = null; }
  }
  function metadata() { if (pendingSeek !== null) seek(pendingSeek); }
  function failure() {
    if (current && audio.error)
      report(new Error(`Audio playback failed (code ${audio.error.code}): ${audio.error.message || "the native player could not decode this preview"}`));
  }
  audio.addEventListener("loadedmetadata", metadata);
  audio.addEventListener("error", failure);
  audio.addEventListener("ended", settle);
  return {
    load(preview, release = releaseMedia) {
      if (disposed) throw new Error("Player disposed");
      clear(); current = {preview, release}; audio.src = preview.url; audio.load();
    },
    async play() {
      if (!current || disposed) return;
      const attempt = generation;
      try { await audio.play(); }
      catch (error) { if (attempt === generation) report(new Error(`Press Play to start preview: ${error?.message || error}`)); }
    },
    pause: () => audio.pause(),
    seek, clear,
    // Resolves when the loaded media finishes playing or is replaced.
    ended() {
      if (!current) return Promise.resolve();
      return new Promise((resolve) => {
        const done = () => { waiters.delete(done); resolve(); };
        waiters.add(done);
      });
    },
    dispose() {
      if (disposed) return;
      disposed = true; clear();
      audio.removeEventListener("loadedmetadata", metadata);
      audio.removeEventListener("error", failure);
      audio.removeEventListener("ended", settle);
    },
  };
}
