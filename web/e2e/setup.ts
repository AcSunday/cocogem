import { execFileSync, spawn } from "node:child_process";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { createServer } from "node:net";

export default async function setup() {
  const dir = await mkdtemp(join(tmpdir(), "cocogem-e2e-"));
  const binary = join(dir, "httpapi.test");
  try {
    execFileSync("go", ["test", "-c", "-o", binary, "./internal/httpapi"], {
      cwd: join(process.cwd(), ".."),
      stdio: "pipe",
    });
  } catch (error) {
    await rm(dir, { recursive: true, force: true });
    throw error;
  }
  const probe = createServer();
  await new Promise<void>((resolve) => probe.listen(0, "127.0.0.1", resolve));
  const address = probe.address();
  if (!address || typeof address === "string") throw new Error("No test port");
  const port = address.port;
  await new Promise<void>((resolve) => probe.close(() => resolve()));
  const child = spawn(
    binary,
    ["-test.run=^TestBrowserServer$", "-test.timeout=0"],
    {
      cwd: process.cwd(),
      env: {
        ...process.env,
        COCOGEM_E2E: "1",
        COCOGEM_DATA_DIR: dir,
        COCOGEM_E2E_LISTEN: `127.0.0.1:${port}`,
        COCOGEM_E2E_WEB_DIR: join(process.cwd(), "dist"),
      },
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
    await new Promise<string>((resolve, reject) => {
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
        const match = data.toString().match(/Cocogem: (http:\/\/[^\s]+)/);
        if (match) {
          clearTimeout(timer);
          resolve(match[1]);
        }
      });
    });
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
