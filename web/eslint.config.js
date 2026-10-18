import js from "@eslint/js";
import ts from "typescript-eslint";
import globals from "globals";
export default ts.config(
  js.configs.recommended,
  ...ts.configs.recommended,
  {
    files: ["src/**/*.{ts,tsx}"],
    languageOptions: { globals: { ...globals.browser, ...globals.node } },
    rules: { "@typescript-eslint/no-explicit-any": "error" },
  },
  { ignores: ["src/api/schema.ts"] },
);
