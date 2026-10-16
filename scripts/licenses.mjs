import { execFileSync } from 'node:child_process';
import { readFileSync, readdirSync, writeFileSync } from 'node:fs';
import { resolve, join } from 'node:path';
import { fileURLToPath } from 'node:url';

export function licenseFiles(dir) {
  return readdirSync(dir).filter(name => /^(licen[sc]e|copying)([.-].*)?$/i.test(name)).sort();
}

export function generate() {
  const blocks = ['Cocogem third-party notices\nGenerated from pinned production dependencies.\n'];
  const root = resolve('.');
  const modules = new Set(execFileSync('go', ['list', '-deps', '-f', '{{with .Module}}{{.Path}}{{end}}', './cmd/cocogem-server'], { encoding: 'utf8' }).trim().split('\n').filter(name => name && name !== 'github.com/AcSunday/cocogem'));
  for (const name of [...modules].sort()) {
    const info = JSON.parse(execFileSync('go', ['list', '-m', '-json', name], { encoding: 'utf8' }));
    const files = licenseFiles(info.Dir);
    if (!files.length) throw new Error(`Missing Go license: ${name}`);
    blocks.push(`\n=== Go ${name} ${info.Version} ===\n` + files.map(file => readFileSync(join(info.Dir, file), 'utf8')).join('\n'));
  }
  const goroot = execFileSync('go', ['env', 'GOROOT'], { encoding: 'utf8' }).trim();
  blocks.push('\n=== Go standard library ===\n' + readFileSync(join(goroot, 'LICENSE'), 'utf8'));
  const tree = JSON.parse(execFileSync('npm', ['ls', '--omit=dev', '--all', '--long', '--json'], { cwd: join(root, 'web'), encoding: 'utf8' }));
  const seen = new Set();
  function visit(node) {
    for (const [name, dependency] of Object.entries(node.dependencies ?? {})) {
      const key = `${name}@${dependency.version}`;
      if (!seen.has(key)) {
        seen.add(key);
        const files = licenseFiles(dependency.path);
        if (!files.length) throw new Error(`Missing npm license: ${key}`);
        blocks.push(`\n=== npm ${key} (${dependency.license ?? 'see license'}) ===\n` + files.map(file => readFileSync(join(dependency.path, file), 'utf8')).join('\n'));
        visit(dependency);
      }
    }
  }
  visit(tree);
  writeFileSync('THIRD_PARTY_NOTICES.txt', blocks.join('\n'));
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) generate();
