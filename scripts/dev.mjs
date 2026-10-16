import { spawn } from 'node:child_process';

const children = [];
let exiting = false;
function stop(code = 0) {
  if (exiting) return;
  exiting = true;
  for (const child of children) child.kill('SIGTERM');
  process.exitCode = code;
}
for (const signal of ['SIGINT', 'SIGTERM']) process.on(signal, () => stop());
const frontend = spawn(process.execPath, ['node_modules/vite/bin/vite.js', '--host', '127.0.0.1'], { cwd:'web', stdio: 'inherit' });
children.push(frontend);
const backend = spawn('./dist/cocogem-server', ['--listen', '127.0.0.1:8741'], { stdio: 'inherit' });
children.push(backend);
for (const child of children) {
  child.on('error', error => { process.stderr.write(`${error.message}\n`); stop(1); });
  child.on('exit', code => { if (!exiting) stop(code ?? 1); });
}
