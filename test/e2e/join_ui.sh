#!/usr/bin/env bash
# Adding a machine in the web UI, joining it from a shell with the command the page shows, and the page
# changing to "ready" by itself (no reload) once the link calls in. Real hub, real link, real Chromium.
#   PLAYWRIGHT_MODULE=/path/to/playwright test/e2e/join_ui.sh
set -uo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
GO="${GO:-$(command -v go || echo "$HOME/.local/go/bin/go")}"; export PATH="$PATH:$(dirname "$GO")"
OUT="$ROOT/test/e2e/out/join-ui"; PORT="${PORT:-17850}"; H="http://127.0.0.1:$PORT"
CLEAN="env -u TMUX -u TMUX_PANE -u HERDR_ENV -u HERDR_PANE_ID -u HERDR_SOCKET_PATH"
rm -rf "$OUT"; mkdir -p "$OUT"/{data,bin,home}
BIN="$OUT/bin/handloom"; (cd "$ROOT" && "$GO" build -o "$BIN" ./cmd/handloom) || exit 1
$CLEAN "$BIN" hub init --data "$OUT/data" >/dev/null 2>&1
PW="$($CLEAN "$BIN" hub create-owner Maya --data "$OUT/data" | sed -n 2p)"
HANDLOOM_BASE_URL="$H" HANDLOOM_INSECURE=1 HANDLOOM_ADDR="127.0.0.1:$PORT" $CLEAN "$BIN" hub serve --data "$OUT/data" > "$OUT/hub.log" 2>&1 & HUB=$!
trap 'kill $HUB $LINK 2>/dev/null' EXIT
for i in $(seq 1 40); do curl -sf "$H/healthz" >/dev/null 2>&1 && break; sleep 0.25; done
cat > "$OUT/page.js" <<'JS'
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || 'playwright');
const { execSync, spawn } = require('child_process');
(async () => {
  const [base, pw, bin, home] = process.argv.slice(2);
  const browser = await chromium.launch({ args: ['--no-sandbox'] });
  const page = await (await browser.newContext({ viewport: { width: 1300, height: 900 } })).newPage();
  const errors = []; page.on('pageerror', (e) => errors.push(String(e)));
  await page.goto(base + '/login'); await page.fill('input[name=name]', 'Maya'); await page.fill('input[name=password]', pw);
  await page.click('button[type=submit]'); await page.waitForLoadState('networkidle');
  await page.goto(base + '/devices');
  await page.fill('form[action="/devices"] input[name=name]', 'ui-box');
  await page.fill('form[action="/devices"] input[name=current_password]', pw);
  await page.click('form[action="/devices"] button[type=submit]'); await page.waitForLoadState('networkidle');
  const body = await page.content();
  const cmd = (await page.locator('.attention .inset').first().innerText()).trim();
  console.log('COMMAND:', cmd.replace(/hvj_\S+/, 'hvj_...'));
  if (!/install\.sh \| sh -s -- join /.test(cmd) || !/Already installed/.test(body)) { console.error('the page does not show the one command'); process.exit(1); }
  const waiting = await page.locator('#machines').innerText();
  console.log('WAITING:', /waiting for it to join/.test(waiting));
  // the machine joins (the same thing the pasted command does after installing), but its link is not running yet
  const tok = cmd.match(/(hvj_\S+)/)[1];
  execSync(`${bin} join ${base} ${tok} --no-start`, { env: { ...process.env, HANDLOOM_HOME: home }, stdio: 'pipe' });
  await page.waitForFunction(() => /joined, link not running/.test(document.querySelector('#machines').innerText), null, { timeout: 15000 });
  console.log('JOINED-NOT-RUNNING: seen on the page without a reload');
  // the link starts: the page says ready by itself
  const link = spawn(bin, ['link', 'run'], { env: { ...process.env, HANDLOOM_HOME: home }, stdio: 'ignore', detached: false });
  await page.waitForFunction(() => document.querySelector('#machines [data-ready]') !== null, null, { timeout: 30000 });
  const announced = await page.locator('#machine-announce').innerText();
  console.log('READY: seen on the page without a reload; announced:', JSON.stringify(announced));
  await page.screenshot({ path: process.argv[7] });
  link.kill();
  await browser.close();
  if (errors.length) { console.error(errors); process.exit(1); }
})().catch((e) => { console.error(String(e)); process.exit(1); });
JS
PLAYWRIGHT_MODULE="${PLAYWRIGHT_MODULE:-playwright}" HANDLOOM_TOKEN="" node "$OUT/page.js" "$H" "$PW" "$BIN" "$OUT/home" "$OUT" "$OUT/ready.png" 2>&1 | sed 's/^/   /'
RC=${PIPESTATUS[0]}
LINK=""; pkill -P $$ -f "handloom link run" 2>/dev/null || true
[ "$RC" = 0 ] && { echo "PASS"; exit 0; } || { echo "FAIL"; exit 1; }
