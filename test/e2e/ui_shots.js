// Screenshots of the web UI's pages for looking at the layout, light and dark, wide, folded and on a phone.
//   PLAYWRIGHT_MODULE=/path/to/playwright node ui_shots.js <base-url> <name> <password> <out-dir>
const path = require('path');
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || 'playwright');
(async () => {
  const [base, name, password, out] = process.argv.slice(2);
  const browser = await chromium.launch({ executablePath: process.env.CHROMIUM || undefined, args: ['--no-sandbox'] });
  const errors = [];
  const pages = ['command', 'inbox', 'jobs', 'agents', 'profiles', 'profiles/new', 'devices', 'settings', 'connect'];
  async function session(viewport, scheme, folded) {
    const ctx = await browser.newContext({ viewport, colorScheme: scheme });
    if (folded) await ctx.addInitScript(() => localStorage.setItem('handloom-sidebar', 'collapsed'));
    const page = await ctx.newPage();
    page.on('pageerror', (e) => errors.push(String(e)));
    page.on('console', (m) => { if (m.type() === 'error') errors.push(m.text()); });
    await page.goto(base + '/login');
    await page.fill('input[name=name]', name);
    await page.fill('input[name=password]', password);
    await page.click('button[type=submit]');
    await page.waitForLoadState('networkidle');
    return { ctx, page };
  }
  const shot = (page, f) => page.screenshot({ path: path.join(out, f), fullPage: false });
  // wide, light and dark
  for (const scheme of ['light', 'dark']) {
    const { ctx, page } = await session({ width: 1440, height: 900 }, scheme, false);
    for (const p of pages) { await page.goto(`${base}/${p}`); await page.waitForLoadState('networkidle'); await shot(page, `${scheme}-${p.replace('/', '-')}.png`); }
    await page.goto(`${base}/command`);
    await page.click('summary[aria-label="Your account"]'); await shot(page, `${scheme}-menu-account.png`);
    await page.click('summary[aria-label="Theme"]'); await shot(page, `${scheme}-menu-theme.png`);
    await ctx.close();
  }
  // folded
  { const { ctx, page } = await session({ width: 1440, height: 900 }, 'light', true); await page.goto(base + '/inbox'); await shot(page, 'folded-inbox.png');
    await page.click('[data-sidebar-toggle]'); await page.waitForTimeout(300); await shot(page, 'folded-then-opened.png'); await ctx.close(); }
  // phone
  { const { ctx, page } = await session({ width: 390, height: 844 }, 'light', false);
    await page.goto(base + '/command'); await shot(page, 'phone-command.png');
    await page.click('[data-sidebar-toggle]'); await page.waitForTimeout(300); await shot(page, 'phone-menu.png');
    await page.keyboard.press('Escape'); await page.goto(base + '/jobs'); await shot(page, 'phone-jobs.png'); await ctx.close(); }
  await browser.close();
  if (errors.length) { console.error('page errors:', [...new Set(errors)]); process.exit(1); }
  console.log('SHOTS-OK');
})().catch((e) => { console.error(e); process.exit(1); });
