import { spawn } from "node:child_process";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { createServer } from "node:net";

export default async function setup() {
  const dir = await mkdtemp(join(tmpdir(), "cocogem-e2e-"));
  const probe = createServer();
  await new Promise<void>((resolve) => probe.listen(0, "127.0.0.1", resolve));
  const address = probe.address();
  if (!address || typeof address === "string") throw new Error("No test port");
  const port = address.port;
  await new Promise<void>((resolve) => probe.close(() => resolve()));
  const child = spawn(
    "../dist/cocogem-server",
    ["--demo", "--demo-pace", "0", "--listen", `127.0.0.1:${port}`, "--web-dir", "dist"],
    {
      cwd: process.cwd(),
      env: { ...process.env, COCOGEM_DATA_DIR: dir },
      stdio: ["ignore", "pipe", "pipe"],
    },
  );
  let stderr = "";
  const cleanup = async () => {
    if (child.exitCode === null && child.signalCode === null) {
      const exited = new Promise((resolve) => child.once("exit", resolve));
      child.kill("SIGTERM");
      const force = setTimeout(() => child.kill("SIGKILL"), 5000);
      await exited;
      clearTimeout(force);
    }
    await rm(dir, { recursive: true, force: true });
  };
  child.stderr.on("data", (data) => {
    stderr += data.toString();
  });
  try {
    const token = await new Promise<string>((resolve, reject) => {
      const timer = setTimeout(
        () => reject(new Error("Server startup timeout: " + stderr)),
        15000,
      );
      child.once("exit", (code) => {
        clearTimeout(timer);
        reject(new Error("Server exited: " + code + " " + stderr));
      });
      child.once("error", (error) => {
        clearTimeout(timer);
        reject(error);
      });
      child.stdout.on("data", (data) => {
        const match = data.toString().match(/#bootstrap=([^\s]+)/);
        if (match) {
          clearTimeout(timer);
          resolve(match[1]);
        }
      });
    });
    process.env.COCOGEM_TEST_TOKEN = token;
    process.env.COCOGEM_TEST_BASE = `http://127.0.0.1:${port}`;
    for (let count = 0; count < 30; count++) {
      try {
        await fetch(process.env.COCOGEM_TEST_BASE);
        break;
      } catch {
        await new Promise((resolve) => setTimeout(resolve, 100));
      }
    }
    return cleanup;
  } catch (error) {
    await cleanup();
    throw error;
  }
}
