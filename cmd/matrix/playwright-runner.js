// Host-side Playwright driver runner: the composer http-mcp delegates to.
// Reads the LT credential below the boundary (never printed) and connects via
// Playwright's own driver (chromium.connect -> JsonPipeTransport -> driver process).
const fs = require('fs');
const { chromium } = require('playwright-core');
const cred = JSON.parse(fs.readFileSync(process.argv[2], 'utf8')); // {username, accessKey}
const caps = {
  browserName: 'Chrome', browserVersion: 'latest',
  'LT:Options': {
    platform: 'Windows 11', build: 'http-mcp matrix sweep',
    name: 'playwright-chrome-desktop-web (host driver)',
    user: cred.username, accessKey: cred.accessKey, network: true, video: true,
  },
};
(async () => {
  const ws = `wss://cdp.lambdatest.com/playwright?capabilities=${encodeURIComponent(JSON.stringify(caps))}`;
  const browser = await chromium.connect(ws);
  const page = await (await browser.newContext()).newPage();
  await page.goto('https://www.lambdatest.com');
  const title = await page.title();
  await browser.close();
  console.log('PLAYWRIGHT OK  title=' + JSON.stringify(title));
})().catch(e => { console.error('PLAYWRIGHT FAIL ' + e.message); process.exit(1); });
