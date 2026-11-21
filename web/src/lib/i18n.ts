import { useSyncExternalStore } from "react";

export type Language = "zh-CN" | "en";

export const LANGUAGE_STORAGE_KEY = "cocogem-language";

let fallbackLanguage: Language = "zh-CN";
const listeners = new Set<() => void>();

function normalizeLanguage(value: string | null | undefined): Language {
  return value === "en" ? "en" : "zh-CN";
}

function syncDocumentLanguage(lang: Language) {
  if (typeof document === "undefined") return;
  document.documentElement.lang = lang;
  document.documentElement.dataset.language = lang;
  if (
    document.title.startsWith("Cocogem") ||
    !document.title
  ) {
    document.title =
      lang === "en"
        ? "Cocogem · Collaborative Workspace"
        : "Cocogem · 协作工作空间";
  }
}

export function getLanguage(): Language {
  try {
    if (typeof localStorage !== "undefined") {
      const raw = localStorage.getItem(LANGUAGE_STORAGE_KEY);
      if (raw === "en" || raw === "zh-CN") {
        fallbackLanguage = raw;
        return raw;
      }
      fallbackLanguage = "zh-CN";
      return "zh-CN";
    }
  } catch {
    // Ignore storage read errors in restricted environments.
  }
  return fallbackLanguage;
}

export function setLanguage(next: Language): void {
  const normalized = normalizeLanguage(next);
  fallbackLanguage = normalized;
  try {
    if (typeof localStorage !== "undefined") {
      localStorage.setItem(LANGUAGE_STORAGE_KEY, normalized);
    }
  } catch {
    // Ignore storage write errors in restricted environments.
  }
  syncDocumentLanguage(normalized);
  listeners.forEach((listener) => listener());
}

export function subscribeLanguage(listener: () => void): () => void {
  listeners.add(listener);
  const onStorage = (event: StorageEvent) => {
    if (event.key === LANGUAGE_STORAGE_KEY || event.key === null) {
      syncDocumentLanguage(getLanguage());
      listener();
    }
  };
  if (typeof window !== "undefined") {
    window.addEventListener("storage", onStorage);
  }
  return () => {
    listeners.delete(listener);
    if (typeof window !== "undefined") {
      window.removeEventListener("storage", onStorage);
    }
  };
}

export function useLanguage(): [Language, (next: Language) => void] {
  const lang = useSyncExternalStore(
    subscribeLanguage,
    getLanguage,
    getLanguage,
  );
  return [lang, setLanguage];
}

export function isEnglish(): boolean {
  return getLanguage() === "en";
}

export function t(zh: string, en: string): string {
  return getLanguage() === "en" ? en : zh;
}

const backendMessageTranslations: Record<string, string> = {
  请先设置访问密码: "Please set a workspace password first.",
  访问密码已设置: "Workspace password is already set.",
  需要登录: "Authentication required.",
  密码不正确: "Incorrect password.",
  "登录尝试过多，请稍后再试": "Too many login attempts. Please try again later.",
  "密码长度需为 12 到 128 个字符": "Password must be 12 to 128 characters.",
  两次输入的密码不一致: "Passwords do not match.",
  接口不存在: "API endpoint not found.",
  不支持的请求方法: "Unsupported request method.",
  请求字段或JSON格式无效: "Invalid request fields or JSON format.",
  "请求字段或 JSON 格式无效": "Invalid request fields or JSON format.",
  "请求必须是单个 JSON 对象": "Request body must be a single JSON object.",
  "配置已更新，请刷新后重新编辑":
    "Configuration was updated elsewhere. Refresh and try again.",
  缺少协作空间名称: "Project name is required.",
  缺少会话标题: "Conversation title is required.",
  内容不能为空: "Content cannot be empty.",
  目录不存在: "Directory does not exist.",
  目录不存在或无法访问: "Directory does not exist or cannot be accessed.",
  路径不是目录: "Path is not a directory.",
  "请选择文件夹，不能选择文件": "Please choose a folder, not a file.",
  无法读取该目录: "Cannot read this directory.",
  权限模式无效: "Invalid permission mode.",
  该会话不在回收站中: "This conversation is not in the trash.",
  该会话已在回收站中: "This conversation is already in the trash.",
  "会话正在运行，请先停止":
    "A collaboration run is active. Please stop it first.",
  "保留天数需在 1 到 3650 之间": "Retention days must be between 1 and 3650.",
  当前没有可应用的候选变更: "No candidate changes are ready to apply.",
  当前没有可丢弃的候选变更: "No candidate changes to discard.",
};

export function translateBackendMessage(raw: string): string {
  if (!isEnglish() || !raw) return raw;
  const trimmed = raw.trim();
  if (backendMessageTranslations[trimmed]) {
    return backendMessageTranslations[trimmed];
  }
  const tooLarge = trimmed.match(/^请求超过\s*(\d+)\s*KiB$/);
  if (tooLarge) {
    return `Request exceeds ${tooLarge[1]} KiB.`;
  }
  return raw;
}

syncDocumentLanguage(getLanguage());
