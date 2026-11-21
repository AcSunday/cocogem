// Apply the local preference before the page paints, including before React loads.
// A removed theme (e.g. deepseek) falls back to the system pair.
try {
  localStorage.removeItem("cocogem.member-colors");
  const appearance = localStorage.getItem("cocogem-appearance");
  if (["light", "dark", "system", "warm"].includes(appearance)) {
    document.documentElement.dataset.appearance = appearance;
  }
  const language = localStorage.getItem("cocogem-language");
  if (language === "en" || language === "zh-CN") {
    document.documentElement.lang = language;
    document.title =
      language === "en"
        ? "Cocogem · Collaborative Workspace"
        : "Cocogem · 协作工作空间";
  }
} catch {
  // System appearance still works when browser storage is unavailable.
}
