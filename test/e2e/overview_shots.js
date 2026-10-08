// Pictures of the overview page's diagrams for the README (light and dark).
//   PLAYWRIGHT_MODULE=/path/to/playwright node test/e2e/overview_shots.js docs/img
const path = require('path');
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || 'playwright');
const root = path.resolve(__dirname, '..', '..');
const parts = [
  ['topology', 'section[aria-labelledby=topo] .diagram'],
  ['flow', 'section[aria-labelledby=flow] .lanes-wrap'],
  ['teams', 'section[aria-labelledby=teams] .diagram'],
  ['team-day', 'section[aria-labelledby=teams] .story'],
  ['knowledge', 'section[aria-labelledby=know] .pipe'],
  ['layers', 'section[aria-labelledby=arch] .layers'],
];
(async () => {
  const out = process.argv[2] || path.join(root, 'docs', 'img');
  const browser = await chromium.launch({ executablePath: process.env.CHROMIUM || undefined, args: ['--no-sandbox', '--allow-file-access-from-files'] });
  for (const theme of ['light', 'dark']) {
    const page = await browser.newPage({ viewport: { width: 1180, height: 900 }, deviceScaleFactor: 2, colorScheme: theme });
    await page.goto('file://' + path.join(root, 'docs', 'overview.html'));
    await page.evaluate((t) => document.documentElement.setAttribute('data-theme', t), theme);
    await page.waitForTimeout(1200);
    for (const [name, sel] of parts) {
      const el = await page.$(sel);
      if (!el) { console.error('missing', sel); process.exit(1); }
      await el.screenshot({ path: path.join(out, `${name}-${theme}.png`) });
    }
    await page.close();
  }
  await browser.close();
  console.log('OK');
})();
