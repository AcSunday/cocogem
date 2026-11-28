import {
  sourceOriginalImageURL,
  threadFileRawURL,
  type Source,
} from "../api/client";

export type FileRef = {
  path: string;
  startLine?: number;
  endLine?: number;
  tab?: "file" | "diff";
  label?: string;
};

export type ImagePreview = {
  src: string;
  title: string;
  downloadUrl?: string;
  sourceId?: string;
  path?: string;
};

const KNOWN_FILENAMES = new Set([
  "makefile",
  "dockerfile",
  "rakefile",
  "gemfile",
  "procfile",
  "license",
  "go.mod",
  "go.sum",
  "package.json",
  "tsconfig.json",
  "cargo.toml",
  "cargo.lock",
  "pyproject.toml",
  ".gitignore",
  ".env.example",
  ".env.sample",
  ".env.template",
]);

const CODE_AND_DOC_EXTS = new Set([
  "go",
  "ts",
  "tsx",
  "js",
  "jsx",
  "mjs",
  "cjs",
  "py",
  "rs",
  "java",
  "kt",
  "swift",
  "c",
  "cc",
  "cpp",
  "h",
  "hpp",
  "cs",
  "rb",
  "php",
  "sh",
  "bash",
  "zsh",
  "sql",
  "css",
  "scss",
  "less",
  "html",
  "htm",
  "svg",
  "xml",
  "json",
  "jsonl",
  "yaml",
  "yml",
  "toml",
  "ini",
  "conf",
  "cfg",
  "md",
  "mdx",
  "txt",
  "log",
  "diff",
  "patch",
  "csv",
  "tsv",
  "env",
  "png",
  "jpg",
  "jpeg",
  "gif",
  "webp",
  "bmp",
  "ico",
  "pdf",
]);

const IMAGE_EXTS = new Set([
  "png",
  "jpg",
  "jpeg",
  "gif",
  "webp",
  "svg",
  "bmp",
  "ico",
]);

const LINE_HASH_RE = /#L(\d+)(?:-L?(\d+))?$/i;
const LINE_COLON_RE = /:(\d+)(?:-(\d+))?(?::\d+)?$/;
const SAFE_DATA_IMAGE_RE = /^data:image\/(?:png|jpeg|gif|webp);base64,/i;

export function isImageMediaOrPath(
  pathOrMedia?: string | null,
  kind?: string | null,
): boolean {
  if (kind === "image") return true;
  if (!pathOrMedia) return false;
  const trimmed = pathOrMedia.trim().toLowerCase();
  if (trimmed.startsWith("image/")) return true;
  const clean = trimmed.split(/[?#]/)[0] ?? "";
  const dot = clean.lastIndexOf(".");
  if (dot < 0) return false;
  return IMAGE_EXTS.has(clean.slice(dot + 1));
}

export function stripFileScheme(raw: string): string {
  const trimmed = raw.trim();
  if (!trimmed.toLowerCase().startsWith("file://")) return trimmed;
  try {
    const parsed = new URL(trimmed);
    if (parsed.protocol === "file:" && parsed.pathname) {
      return decodeURIComponent(parsed.pathname) + parsed.hash;
    }
  } catch {
    // Fall back to direct prefix stripping for synthetic/relative file URLs.
  }
  return trimmed.slice("file://".length);
}

/**
 * Parses a candidate file reference such as:
 * - `internal/httpapi/thread_files.go:10-25`
 * - `web/src/components/Markdown.tsx#L12-L40`
 * - `file:///Users/sunday/Workspace/cocogem/Makefile#L5`
 * - `README.md`
 * Returns null for web URLs, timestamps, version numbers, or ordinary prose.
 */
export function parseFileRef(raw: string, label?: string): FileRef | null {
  let text = raw.trim();
  if (!text || text.length > 512) return null;
  if (/^[\s("'[]|[\s)"'\]]$/.test(text)) {
    text = text.replace(/^["'([]+/, "").replace(/["')\],.;:!?]+$/, "");
  }
  if (!text) return null;
  const hadFileScheme = text.toLowerCase().startsWith("file://");
  if (hadFileScheme) {
    text = stripFileScheme(text);
  }
  if (
    /^(?:https?|mailto|tel|javascript|data|blob):/i.test(text) ||
    text.startsWith("#") ||
    text.startsWith("@") ||
    /\s/.test(text)
  ) {
    return null;
  }

  let startLine: number | undefined;
  let endLine: number | undefined;
  let basePath = text;

  const hashMatch = LINE_HASH_RE.exec(basePath);
  if (hashMatch) {
    startLine = Number(hashMatch[1]);
    if (hashMatch[2]) endLine = Number(hashMatch[2]);
    basePath = basePath.slice(0, hashMatch.index);
  } else {
    const colonMatch = LINE_COLON_RE.exec(basePath);
    if (colonMatch) {
      startLine = Number(colonMatch[1]);
      if (colonMatch[2]) endLine = Number(colonMatch[2]);
      basePath = basePath.slice(0, colonMatch.index);
    }
  }

  if (!basePath || basePath.endsWith("/")) return null;
  if (startLine && endLine && endLine < startLine) {
    endLine = startLine;
  }

  // Reject version strings like v1.2.3 or pure numeric dotted values like 127.0.0.1
  if (/^v?\d+(?:\.\d+)+$/i.test(basePath)) return null;
  // Reject host:port patterns without path separator unless the filename itself has a code extension
  const fileName = basePath.split("/").pop() ?? "";
  const lowerName = fileName.toLowerCase();
  const dotIndex = lowerName.lastIndexOf(".");
  const ext = dotIndex > 0 ? lowerName.slice(dotIndex + 1) : "";

  if (
    !hadFileScheme &&
    !basePath.includes("/") &&
    !KNOWN_FILENAMES.has(lowerName) &&
    !CODE_AND_DOC_EXTS.has(ext)
  ) {
    return null;
  }
  if (
    !hadFileScheme &&
    basePath.includes("/") &&
    !basePath.startsWith("./") &&
    !basePath.startsWith("../") &&
    !basePath.startsWith("/")
  ) {
    // Require either an extension, known filename, line suffix, or a multi-char directory prefix
    // to avoid matching things like `HTTP/1.1` or `2026/11/28`.
    if (/^\d+(?:\/\d+)+$/.test(basePath)) return null;
    if (/^HTTP\/\d/i.test(basePath)) return null;
    if (!ext && !KNOWN_FILENAMES.has(lowerName) && !startLine) return null;
  }
  if (!/^[A-Za-z0-9_./~@-]+$/.test(basePath)) return null;

  return {
    path: basePath,
    ...(startLine ? { startLine } : {}),
    ...(endLine ? { endLine } : {}),
    ...(label ? { label } : {}),
  };
}

export type TextSegment =
  | { kind: "text"; value: string }
  | { kind: "file"; value: string; ref: FileRef };

const TEXT_FILE_TOKEN_RE =
  /(?:file:\/\/[^\s<>()[\]"']+|(?:\.{0,2}\/)?(?:[A-Za-z0-9_@.-]+\/)+[A-Za-z0-9_.-]+(?::\d+(?:-\d+)?(?::\d+)?|#L\d+(?:-L?\d+)?)?|[A-Za-z0-9_-]+\.[A-Za-z0-9]{1,8}(?::\d+(?:-\d+)?(?::\d+)?|#L\d+(?:-L?\d+)?))/g;

/** Splits plain prose into text and clickable file-reference segments without altering layout. */
export function splitTextFileRefs(text: string): TextSegment[] {
  if (!text) return [];
  const out: TextSegment[] = [];
  let lastIndex = 0;
  TEXT_FILE_TOKEN_RE.lastIndex = 0;
  for (const match of text.matchAll(TEXT_FILE_TOKEN_RE)) {
    const raw = match[0];
    const start = match.index ?? 0;
    const prev = start > 0 ? text[start - 1] : "";
    // Do not break URLs (`https://...`), email addresses (`a@b.com`), or property access (`foo.bar.baz()`).
    if (prev === "@" || prev === ":" || prev === "=") continue;
    const after = text[start + raw.length] ?? "";
    if (after === "(" && !raw.includes("/") && !raw.includes(":")) continue;

    // Trim trailing sentence punctuation while keeping line suffixes intact.
    const trimmed = raw.replace(/[.,;!?]+$/, "");
    const ref = parseFileRef(trimmed);
    if (!ref) continue;

    if (start > lastIndex) {
      out.push({ kind: "text", value: text.slice(lastIndex, start) });
    }
    out.push({ kind: "file", value: trimmed, ref });
    lastIndex = start + trimmed.length;
  }
  if (lastIndex < text.length) {
    out.push({ kind: "text", value: text.slice(lastIndex) });
  }
  return out.length ? out : [{ kind: "text", value: text }];
}

/**
 * Resolves a Markdown image `src` into a fetchable authenticated URL inside the
 * thread UI, returning `undefined` when unsafe or empty.
 */
export function resolveMarkdownImageSrc(
  rawSrc?: string,
  threadID?: string,
  sources?: Source[],
): { src: string; matchedSource?: Source; cleanPath?: string } | undefined {
  if (!rawSrc) return undefined;
  const trimmed = rawSrc.trim();
  if (!trimmed) return undefined;
  if (/^(?:javascript|vbscript):/i.test(trimmed)) return undefined;
  if (
    /^https?:\/\//i.test(trimmed) ||
    trimmed.startsWith("/api/") ||
    trimmed.startsWith("blob:") ||
    SAFE_DATA_IMAGE_RE.test(trimmed)
  ) {
    return { src: trimmed };
  }
  const clean = stripFileScheme(trimmed).replace(/^\.\//, "");
  if (sources?.length) {
    for (let i = sources.length - 1; i >= 0; i--) {
      const s = sources[i];
      if (!isImageMediaOrPath(s.media_type || s.path || s.title, s.kind)) {
        continue;
      }
      const normPath = (s.path ?? "").replace(/^\.\//, "");
      if (
        s.id === trimmed ||
        s.path === trimmed ||
        normPath === clean ||
        s.title === trimmed
      ) {
        return {
          src: sourceOriginalImageURL(s.id),
          matchedSource: s,
          cleanPath: s.path || clean,
        };
      }
    }
  }
  if (threadID) {
    const cleanPath = stripFileScheme(trimmed);
    return {
      src: threadFileRawURL(threadID, cleanPath),
      cleanPath,
    };
  }
  return undefined;
}
