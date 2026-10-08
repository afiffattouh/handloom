// Pictures for the README. Two modes:
//   node demo_shots.js <base-url> <name> <password> <out-dir>      the web UI, signed in, light and dark
//   node demo_shots.js --html <file.html> <out.png>                an HTML page (the terminal console rendered by ansi2html.py)
const path = require('path');
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || 'playwright');
(async () => {
  const browser = await chromium.launch({ executablePath: process.env.CHROMIUM || undefined, args: ['--no-sandbox', '--allow-file-access-from-files'] });
  if (process.argv[2] === '--html') {
    const page = await browser.newPage({ viewport: { width: 1400, height: 900 }, deviceScaleFactor: 1.5 });
    await page.goto('file://' + path.resolve(process.argv[3])); await page.waitForTimeout(600);
    await (await page.$('.win')).screenshot({ path: process.argv[4], omitBackground: true });
    await browser.close(); return;
  }
  const [base, name, password, out] = process.argv.slice(2);
  async function open(scheme) {
    const ctx = await browser.newContext({ viewport: { width: 1360, height: 860 }, colorScheme: scheme, deviceScaleFactor: 1 });
    const page = await ctx.newPage();
    await page.goto(base + '/login'); await page.fill('input[name=name]', name); await page.fill('input[name=password]', password);
    await page.click('button[type=submit]'); await page.waitForLoadState('networkidle');
    return page;
  }
  const go = async (page, p) => { await page.goto(base + p); await page.waitForLoadState('networkidle'); await page.waitForTimeout(500); };
  for (const scheme of ['light', 'dark']) {
    const page = await open(scheme);
    await go(page, '/command'); await page.screenshot({ path: path.join(out, `command-${scheme}.png`) });
    if (scheme === 'light') {
      await go(page, '/inbox'); await page.screenshot({ path: path.join(out, 'inbox.png') });
      await go(page, '/jobs/1'); await page.screenshot({ path: path.join(out, 'job.png') });
      await go(page, '/agents/lead-ui'); await page.waitForTimeout(3500); await page.screenshot({ path: path.join(out, 'agent.png') });
      await go(page, '/profiles/starters'); await page.screenshot({ path: path.join(out, 'starters.png') });
      await go(page, '/profiles/new'); await page.screenshot({ path: path.join(out, 'profile-new.png') });
    }
    await page.context().close();
  }
  await browser.close();
  console.log('SHOTS-OK');
})().catch((e) => { console.error(e); process.exit(1); });
