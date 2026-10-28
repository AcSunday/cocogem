// Apply the local preference before the page paints, including before React loads.
try {
  const appearance = localStorage.getItem("cocogem-appearance");
  if (["light", "dark", "system"].includes(appearance)) {
    document.documentElement.dataset.appearance = appearance;
  }
} catch {
  // System appearance still works when browser storage is unavailable.
}
