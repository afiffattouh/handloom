// Browser half of the milestone A demo: sign in, answer the lead's question
// by clicking the option, and check that the inbox updated by itself over the
// event stream. Needs the `playwright` package and a Chromium.
//   node a_browser.js <base-url> <name> <password> <option-to-click> <screenshot-dir>
const path = require('path');
const mod = process.env.PLAYWRIGHT_MODULE || 'playwright';
const { chromium } = require(mod);
(async () => {
  const [base, name, password, option, shots] = process.argv.slice(2);
  const browser = await chromium.launch({
    executablePath: process.env.CHROMIUM || undefined,
    args: ['--no-sandbox'],
  });
  const ctx = await browser.newContext({ viewport: { width: 420, height: 900 } });
  const page = await ctx.newPage();
  const errors = [];
  page.on('pageerror', (e) => errors.push(String(e)));
  await page.goto(base + '/login');
  await page.fill('input[name=name]', name);
  await page.fill('input[name=password]', password);
  await page.click('button[type=submit]');
  await page.waitForURL('**/inbox');
  await page.waitForSelector('text=lead asks');
  await page.screenshot({ path: path.join(shots, 'inbox-question.png'), fullPage: true });
  await page.click(`button[name=answer][value="${option}"]`);
  await page.waitForSelector('text=Answer sent');
  await page.waitForSelector('text=Nothing needs you.');
  await page.screenshot({ path: path.join(shots, 'inbox-answered.png'), fullPage: true });

  // The page updates by itself: a second question appears without a reload.
  console.log('READY-FOR-SECOND');
  await new Promise((r) => process.stdin.once('data', r));
  await page.waitForSelector('text=Second question from the lead', { timeout: 10000 });
  await page.screenshot({ path: path.join(shots, 'inbox-live.png'), fullPage: true });
  if (errors.length) { console.error('page errors:', errors); process.exit(1); }
  console.log('BROWSER-OK');
  await browser.close();
})().catch((e) => { console.error(e); process.exit(1); });
