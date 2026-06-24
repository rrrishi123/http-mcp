// Host-side Playwright driver runner: the composer http-mcp delegates to.
//
// http-mcp opens raw channels; /playwright is driver-mediated (the client spawns
// a driver process for the handshake) so it cannot be a raw wsx upgrade. This is
// that driver, in the host. cmd/matrix invokes:  node playwright-runner.js <authfile> <capsJSON>
//
// The LT credential is read from the gitignored auth file BELOW the boundary and
// merged into LT:Options here — it is never an argv, never logged. Requires the
// `playwright-core` package resolvable (run from a playwright-equipped dir or with
// NODE_PATH set); the driver lives in the host (8/pilot), not in http-mcp.
const fs = require('fs');
const pw = require('playwright-core');
const cred = JSON.parse(fs.readFileSync(process.argv[2], 'utf8')); // {username, accessKey}
const caps = JSON.parse(process.argv[3] || '{}');
caps['LT:Options'] = Object.assign({}, caps['LT:Options'], { user: cred.username, accessKey: cred.accessKey });

// Pick the Playwright browser type from the requested browser.
const name = String(caps.browserName || 'Chrome').toLowerCase();
const bt = name.includes('firefox') ? pw.firefox
  : (name.includes('webkit') || name.includes('safari')) ? pw.webkit
    : pw.chromium;

(async () => {
  const ws = `wss://cdp.lambdatest.com/playwright?capabilities=${encodeURIComponent(JSON.stringify(caps))}`;
  const browser = await bt.connect(ws);
  const page = await (await browser.newContext()).newPage();
  // A genuine bit of activity so the video/network artifacts are non-empty.
  await page.goto('https://www.lambdatest.com');
  const title = await page.title();
  await page.waitForTimeout(3000);
  await page.goto('https://www.lambdatest.com/support/docs/');
  await page.waitForTimeout(2000);
  await browser.close();
  console.log('PLAYWRIGHT OK  title=' + JSON.stringify(title));
})().catch(e => { console.error('PLAYWRIGHT FAIL ' + e.message); process.exit(1); });
