(() => {
  // Recover once when a tab still references a previous release's chunks.
  // A bounded retry avoids reload loops during an actual network outage.
  const recover = () => {
    try {
      const key = "komari:asset-recovery";
      const last = Number(sessionStorage.getItem(key) || 0);
      if (Date.now() - last < 60000) return false;
      sessionStorage.setItem(key, String(Date.now()));
      const url = new URL(location.href);
      url.searchParams.set("_komari_reload", String(Date.now()));
      location.replace(url.href);
      return true;
    } catch { return false; }
  };
  window.addEventListener("vite:preloadError", (event) => {
    if (recover()) event.preventDefault();
  });
  window.addEventListener("error", (event) => {
    if (event.target instanceof HTMLScriptElement && event.target.type === "module") recover();
  }, true);
  if ("serviceWorker" in navigator) {
    // Changing the script URL replaces the old root registration without
    // waiting for a CDN's previously cached /sw.js to expire.
    window.addEventListener("load", () => {
      navigator.serviceWorker.register("/komari-sw.js?v=1", {scope: "/", updateViaCache: "none"}).catch(() => {});
    }, {once: true});
  }
})();
