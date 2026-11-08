import { spawn, spawnSync } from 'node:child_process';
import { mkdtempSync, rmSync } from 'node:fs';
import { createServer } from 'node:net';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { chromium } from '../web/node_modules/playwright/index.mjs';

const build = spawnSync('make', ['build'], { stdio: 'inherit' });
if (build.error) throw build.error;
if (build.status !== 0) throw new Error(`make build exited: ${build.status}`);
const probe = createServer();
await new Promise((resolve, reject) => {
  probe.once('error', reject);
  probe.listen(0, '127.0.0.1', resolve);
});
const address = probe.address();
if (!address || typeof address === 'string') throw new Error('no verification port');
await new Promise((resolve, reject) => probe.close(error => error ? reject(error) : resolve()));
const listen = `127.0.0.1:${address.port}`;
const baseURL = `http://${listen}`;
const dir = mkdtempSync(join(tmpdir(), 'cocogem-redesign-browser-'));
const child = spawn('./dist/cocogem-server', ['--listen', listen], { env: { ...process.env, COCOGEM_DATA_DIR: dir }, stdio: ['ignore', 'pipe', 'pipe'] });
const exited = new Promise(resolve => child.once('exit', resolve));
let logs = '';
child.stdout.on('data', data => { logs += data; });
child.stderr.on('data', data => { logs += data; });
let browser;
try {
  let ready = false;
  for (let i = 0; i < 300; i++) {
    try {
      const [api, ui] = await Promise.all([fetch(`${baseURL}/auth/status`), fetch(`${baseURL}/`)]);
      if ([200, 401].includes(api.status) && ui.status === 200) { ready = true; break; }
    } catch { /* development services are still starting */ }
    if (child.exitCode !== null) throw new Error(`verification server exited: ${logs}`);
    await new Promise(resolve => setTimeout(resolve, 100));
  }
  if (!ready) throw new Error(`verification server startup timed out: ${logs}`);
  browser = await chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || 'chrome', headless: true });
  const page = await browser.newPage();
  const errors = [];
  page.on('pageerror', error => errors.push(error.message));
  await page.goto(baseURL);
  await page.getByLabel('设置密码', { exact: true }).fill('redesign-browser-password');
  await page.getByLabel('确认密码', { exact: true }).fill('redesign-browser-password');
  await page.getByRole('button', { name: '设置密码并进入' }).click();
  await page.getByRole('heading', { name: '把问题交给一个团队。' }).waitFor();
  const composer = page.getByLabel('输入问题');
  if (await composer.isDisabled()) throw new Error('first-question composer is disabled');
  await composer.fill('请只回复“已收到”，不要调用工具。');
  await composer.press('Enter');
  await page.waitForURL(/\/w\/[^/]+\/t\/[^/]+$/, { timeout: 90000 });
  // The terminal status can live in a reader-collapsed run disclosure. Match
  // the actual completed state without requiring that disclosure to be open.
  await page.getByTestId('run-status').filter({ hasText: '已完成' }).waitFor({ state: 'attached', timeout: 90000 });
  await page.getByRole('button', { name: '转为任务', exact: true }).click();
  await page.getByLabel('任务标题', { exact: true }).fill('持久任务验收');
  await page.getByRole('button', { name: '保存任务', exact: true }).click();
  await page.getByRole('button', { name: /查看任务 →/ }).click();
  await page.waitForURL(/\/w\/[^/]+\/tasks\/[^/]+$/);
  const taskURL = page.url();
  const workspaceID = taskURL.split('/w/')[1].split('/')[0];
  const taskID = taskURL.split('/tasks/')[1];
  const readBudget = async () => {
    const response = await page.request.get(`${baseURL}/api/v1/tasks/${taskID}/budget`);
    if (!response.ok()) throw new Error(`task budget read failed: ${response.status()}`);
    return response.json();
  };
  const initialBudget = await readBudget();
  await page.getByRole('heading', { name: '持久任务验收' }).waitFor({ timeout: 30000 });
  await page.getByRole('button', { name: '打开会话', exact: true }).first().click();
  const permission = page.getByRole('combobox', { name: '权限模式' });
  await permission.waitFor();
  const originalMode = await permission.textContent();
  await page.getByRole('button', { name: '任务详情' }).click();
  if (page.url() !== taskURL) throw new Error('task detail link did not return to the durable task');
  await page.reload();
  await page.getByRole('heading', { name: '持久任务验收' }).waitFor();
  if (page.url() !== taskURL) throw new Error('task URL changed after refresh');
  for (const label of ['任务设置', '补充目标与执行设置', '参考来源', '验收交付']) {
    if (await page.getByText(label, { exact: true }).count()) throw new Error(`retired task feature still visible: ${label}`);
  }

  await page.goto(`${baseURL}/w/${workspaceID}`);
  await page.getByRole('heading', { name: '把问题交给一个团队。' }).waitFor();
  await page.getByRole('button', { name: '编辑工作空间', exact: true }).click();
  await page.getByRole('dialog', { name: '编辑工作空间' }).waitFor();
  const workspaceDialog = page.getByRole('dialog', { name: '编辑工作空间' });
  for (const label of ['权限与预算', '执行权限模式', '持续授权', '保存项目覆盖']) {
    if (await workspaceDialog.getByText(label, { exact: true }).count()) throw new Error(`retired workspace feature still visible: ${label}`);
  }
  await page.screenshot({ path: join(tmpdir(), 'cocogem-redesign-workspace-settings.png'), fullPage: true });
  await workspaceDialog.getByRole('button', { name: '关闭窗口' }).click();
  await page.getByRole('button', { name: '设置', exact: true }).click();
  const budgetSettings = page.getByRole('dialog', { name: '设置' });
  await budgetSettings.getByRole('tab', { name: '权限', exact: true }).click();
  await budgetSettings.getByRole('radio', { name: /项目内自主/ }).waitFor();
  await budgetSettings.getByRole('tab', { name: '协作', exact: true }).click();
  await budgetSettings.getByLabel(/^自动续办次数/).fill('24');
  const savedDefaults = page.waitForResponse(response => response.url().endsWith('/api/v1/settings') && response.request().method() === 'PATCH');
  await budgetSettings.getByRole('button', { name: '保存协作预算', exact: true }).click();
  if ((await savedDefaults).status() !== 200) throw new Error('global budget update failed');
  await budgetSettings.getByText('协作预算已保存；只影响以后创建的根任务。', { exact: true }).waitFor();
  await budgetSettings.getByRole('button', { name: '关闭窗口' }).click();
  if ((await readBudget()).policy.auto_continuations !== initialBudget.policy.auto_continuations) throw new Error('global defaults changed the existing root budget');
  await page.getByRole('button', { name: '任务清单', exact: true }).click();
  await page.getByRole('heading', { name: '任务清单' }).waitFor();
  await page.getByRole('button', { name: /持久任务验收/ }).first().waitFor();
  if (await page.getByRole('complementary', { name: '导航' }).count()) throw new Error('workspace navigation must be hidden on the global task board');
  await page.screenshot({ path: join(tmpdir(), 'cocogem-redesign-tasks-desktop.png'), fullPage: true });
  await page.getByRole('button', { name: '项目首页', exact: true }).click();
  await page.getByRole('heading', { name: '把问题交给一个团队。' }).waitFor();
  await page.getByText('项目摘要', { exact: true }).waitFor();
  await page.screenshot({ path: join(tmpdir(), 'cocogem-redesign-home-desktop.png'), fullPage: true });
  await page.getByRole('button', { name: '设置', exact: true }).click();
  const settings = page.getByRole('dialog', { name: '设置' });
  await settings.getByRole('tab', { name: '外观' }).click();
  await settings.getByLabel('外观模式').click();
  await page.getByRole('option', { name: '深色', exact: true }).click();
  if (await page.evaluate(() => document.documentElement.dataset.appearance) !== 'dark') throw new Error('appearance setting did not apply');
  await settings.getByLabel('外观模式').click();
  await page.getByRole('option', { name: '跟随系统', exact: true }).click();
  await settings.getByRole('button', { name: '关闭窗口' }).click();

  await page.setViewportSize({ width: 390, height: 844 });
  if (await page.evaluate(() => document.documentElement.scrollWidth > innerWidth)) throw new Error('home overflows on mobile');
  await page.getByText('项目摘要', { exact: true }).waitFor({ state: 'detached' });
  await page.waitForFunction(() => document.querySelector('#composer-input')?.getAttribute('placeholder') === '一起构建什么？');
  await page.screenshot({ path: join(tmpdir(), 'cocogem-redesign-home-mobile.png'), fullPage: true });
  await page.getByRole('button', { name: '任务清单', exact: true }).click();
  await page.getByRole('heading', { name: '任务清单' }).waitFor();
  if (await page.getByRole('complementary', { name: '导航' }).count()) throw new Error('workspace navigation must be hidden on the mobile task board');
  if (await page.evaluate(() => document.documentElement.scrollWidth > innerWidth)) throw new Error('global task list overflows on mobile');
  await page.screenshot({ path: join(tmpdir(), 'cocogem-redesign-tasks-mobile.png'), fullPage: true });

  await page.setViewportSize({ width: 1440, height: 900 });
  await page.getByRole('button', { name: '项目首页', exact: true }).click();
  await page.getByRole('button', { name: '创建工作空间', exact: true }).click();
  await page.getByLabel('名称', { exact: true }).fill('第二个项目');
  await page.getByRole('button', { name: '保存工作空间' }).click();
  if (await page.getByRole('combobox', { name: '权限模式' }).textContent() !== originalMode) throw new Error('creating a workspace changed the global permission');
  await page.getByRole('button', { name: '任务清单', exact: true }).click();
  await page.getByRole('heading', { name: '任务清单' }).waitFor();
  await page.getByRole('button', { name: /持久任务验收/ }).first().waitFor();
  await page.goto(taskURL);
  await page.getByRole('heading', { name: '持久任务验收' }).waitFor();
  if ((await readBudget()).policy.auto_continuations !== initialBudget.policy.auto_continuations) throw new Error('a new workspace changed the existing root budget');
  if (errors.length) throw new Error(`browser errors: ${errors.join('; ')}`);
  console.log(`PASS isolated real-provider first-question journey: ${taskURL}`);
} catch (error) {
  if (browser) {
    const page = browser.contexts()[0]?.pages()[0];
    if (page) {
      const match = page.url().match(/\/t\/([^/?#]+)/);
      if (match) {
        const response = await page.request.get(`${baseURL}/api/v1/threads/${match[1]}`).catch(() => undefined);
        if (response?.ok()) {
          const view = await response.json();
          console.error('verification run states:', JSON.stringify((view.runs || []).map(run => ({
            status: run.status, error: run.error, executor: run.agents?.map(member => member.executor),
          }))));
        }
      }
      await page.screenshot({ path: join(tmpdir(), 'cocogem-redesign-verification-failure.png'), fullPage: true }).catch(() => {});
    }
  }
  console.error('verification service states:', logs.split('\n').filter(line => /run (started|settled)|server stopped/.test(line)).slice(-10).join('\n'));
  throw error;
} finally {
  if (browser) await browser.close();
  if (child.exitCode === null && child.signalCode === null) child.kill('SIGTERM');
  await exited;
  rmSync(dir, { recursive: true, force: true });
}
