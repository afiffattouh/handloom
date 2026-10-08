// Renders the README banner and the GitHub social preview from docs/brand/src.
//   PLAYWRIGHT_MODULE=/path/to/playwright node test/e2e/brand_shots.js
const path = require('path');
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || 'playwright');
const root = path.resolve(__dirname, '..', '..');
(async () => {
  const b = await chromium.launch({ args: ['--no-sandbox', '--allow-file-access-from-files'] });
  for (const [src, out, w, h] of [['banner', 'banner', 1600, 420], ['social-preview', 'social-preview', 1280, 640]]) {
    const p = await b.newPage({ viewport: { width: w, height: h } });
    await p.goto('file://' + path.join(root, 'docs', 'brand', 'src', src + '.html')); await p.waitForTimeout(700);
    await p.screenshot({ path: path.join(root, 'docs', 'brand', out + '.png') }); await p.close();
  }
  await b.close(); console.log('OK');
})();
