import { spawn } from 'node:child_process';
import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { chromium } from '../web/node_modules/playwright/index.mjs';

const dir = mkdtempSync(join(tmpdir(), 'cocogem-redesign-browser-'));
const child = spawn('make', ['dev'], { env: { ...process.env, COCOGEM_DATA_DIR: dir }, stdio: ['ignore', 'pipe', 'pipe'] });
const exited = new Promise(resolve => child.once('exit', resolve));
let logs = '';
child.stdout.on('data', data => { logs += data; });
child.stderr.on('data', data => { logs += data; });
let browser;
try {
  let ready = false;
  for (let i = 0; i < 300; i++) {
    try {
      const [api, ui] = await Promise.all([fetch('http://127.0.0.1:8741/api/v1/system'), fetch('http://127.0.0.1:5173/')]);
      if ([200, 401].includes(api.status) && ui.status === 200) { ready = true; break; }
    } catch { /* development services are still starting */ }
    if (child.exitCode !== null) throw new Error(`make dev exited: ${logs}`);
    await new Promise(resolve => setTimeout(resolve, 100));
  }
  if (!ready) throw new Error(`make dev startup timed out: ${logs}`);
  browser = await chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || 'chrome', headless: true });
  const page = await browser.newPage();
  const errors = [];
  page.on('pageerror', error => errors.push(error.message));
  await page.goto('http://127.0.0.1:5173/');
  await page.getByLabel('设置密码', { exact: true }).fill('redesign-browser-password');
  await page.getByLabel('确认密码', { exact: true }).fill('redesign-browser-password');
  await page.getByRole('button', { name: '设置密码并进入' }).click();
  await page.getByRole('heading', { name: '把问题交给一个团队。' }).waitFor();
  const composer = page.getByLabel('输入问题');
  if (await composer.isDisabled()) throw new Error('first-question composer is disabled');
  await composer.fill('请只回复“已收到”，不要调用工具。');
  await composer.press('Enter');
  await page.waitForURL(/\/w\/[^/]+\/tasks\/[^/]+$/, { timeout: 90000 });
  const taskURL = page.url();
  const workspaceID = taskURL.split('/w/')[1].split('/')[0];
  await page.getByRole('heading', { name: '请只回复“已收到”，不要调用工具。' }).waitFor({ timeout: 30000 });
  await page.getByRole('heading', { name: '项目权限' }).waitFor();
  await page.getByText('项目设置 · 权限、授权与默认预算', { exact: true }).waitFor();
  await page.getByRole('button', { name: '打开执行对话' }).click();
  await page.getByRole('button', { name: '任务详情' }).click();
  if (page.url() !== taskURL) throw new Error('task detail link did not return to the durable task');
  await page.getByRole('heading', { name: '项目权限' }).waitFor();
  await page.reload();
  await page.getByRole('heading', { name: '本任务协作预算' }).waitFor();
  if (page.url() !== taskURL) throw new Error('task URL changed after refresh');

  await page.getByLabel('补充内容').fill('后续请继续只使用简短中文回答。');
  await page.getByRole('button', { name: '保存补充' }).click();
  await page.getByText('后续请继续只使用简短中文回答。', { exact: true }).waitFor();
  const taskBudget = page.getByRole('region', { name: '本任务协作预算' });
  const budget = taskBudget.getByLabel(/^自动续办/);
  await budget.fill('21');
  const savedBudget = page.waitForResponse(response => response.url().includes('/budget') && response.request().method() === 'PATCH');
  await taskBudget.getByRole('button', { name: '保存并用于后续准入' }).click();
  if ((await savedBudget).status() !== 200) throw new Error('task budget update failed');
  await budget.waitFor();
  if (await budget.inputValue() !== '21') throw new Error('task budget was not saved');

  await page.goto(`http://127.0.0.1:5173/w/${workspaceID}`);
  await page.getByText('项目设置 · 权限、授权与默认预算', { exact: true }).click();
  await page.getByLabel('自动续办次数', { exact: true }).fill('24');
  const savedProject = page.waitForResponse(response => response.url().endsWith('/budget-policy') && response.request().method() === 'PATCH');
  await page.getByRole('button', { name: '保存项目覆盖' }).click();
  if ((await savedProject).status() !== 200) throw new Error('project budget override failed');
  await page.getByLabel('自动续办次数', { exact: true }).waitFor();
  if (await page.getByLabel('自动续办次数', { exact: true }).inputValue() !== '24') throw new Error('project budget override was not restored');
  await page.goto(taskURL);
  await page.getByRole('heading', { name: '本任务协作预算' }).waitFor();
  if (await page.getByRole('region', { name: '本任务协作预算' }).getByLabel(/^自动续办/).inputValue() !== '21') throw new Error('project defaults changed existing task budget');
  if (errors.length) throw new Error(`browser errors: ${errors.join('; ')}`);
  console.log(`PASS make dev first-question journey: ${taskURL}`);
} finally {
  if (browser) await browser.close();
  if (child.exitCode === null && child.signalCode === null) child.kill('SIGTERM');
  await exited;
  rmSync(dir, { recursive: true, force: true });
}
