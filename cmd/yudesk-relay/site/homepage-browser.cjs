'use strict';

// Local-only QA against TestHomepagePreview. This file is never served by /site/.
const assert = require('node:assert/strict');
const path = require('node:path');
const { chromium } = require('playwright');

(async () => {
  const [origin, output] = process.argv.slice(2);
  assert.equal(new URL(origin).hostname, '127.0.0.1');
  const browser = await chromium.launch({ headless: true, executablePath: process.env.YUDESK_TEST_BROWSER });
  const failures = [];
  try {
    for (const [width, height] of [[1440, 1000], [1024, 768], [768, 1024], [390, 844], [320, 740]]) {
      const context = await browser.newContext({ viewport: { width, height }, reducedMotion: 'reduce' });
      let statsRequests = 0;
      await context.route('**/*', route => {
        const url = new URL(route.request().url());
        if (url.origin === new URL(origin).origin) return route.continue();
        failures.push('External request: ' + url);
        return route.abort();
      });
      const page = await context.newPage();
      page.on('request', request => { if (new URL(request.url()).pathname === '/api/public-stats') statsRequests++; });
      page.on('pageerror', error => failures.push(error.message));
      page.on('console', message => { if (message.type() === 'error') failures.push(message.text()); });
      page.on('response', response => { if (response.status() >= 400) failures.push(response.status() + ' ' + response.url()); });
      await page.goto(origin, { waitUntil: 'networkidle' });
      assert.equal(await page.locator('h1').count(), 1);
      assert.equal(await page.locator('.download-grid .download').count(), 5);
      assert.equal(await page.locator('.live-status').count(), 1);
      assert.match(await page.locator('#live-online').textContent(), /^\d+$/);
      assert.match(await page.locator('#live-connected').textContent(), /^\d+$/);
      assert.match(await page.locator('#live-sessions').textContent(), /^\d+ 个会话/);
      assert.ok(statsRequests >= 1, width + ': live statistics were not refreshed');
      const firstDownload = await page.locator('.download-grid .download').first().boundingBox();
      assert.ok(firstDownload && firstDownload.y + firstDownload.height < height, width + ': first download must be above fold');
      assert.equal(await page.locator('.steps>li').count(), 3);
      assert.equal(await page.locator('.product-motto').textContent(), '低延迟高响应');
      assert.equal(await page.locator('.icp-link').getAttribute('href'), 'https://beian.miit.gov.cn/');
      await page.locator('.icp-link img').evaluate(img => img.decode());
      assert.deepEqual(await page.locator('.icp-link img').evaluate(img => [img.naturalWidth, img.naturalHeight]), [14, 14]);
      await page.keyboard.press('Tab');
      assert.equal(await page.locator(':focus').textContent(), '跳到下载区');
      for (const id of ['features', 'how-it-works', 'support', 'downloads']) {
        await page.evaluate(id => document.getElementById(id).scrollIntoView(), id);
        const overflow = await page.evaluate(() => ({ viewport: innerWidth, width: document.documentElement.scrollWidth }));
        assert.ok(overflow.width <= overflow.viewport, width + ': horizontal overflow ' + JSON.stringify(overflow));
      }
      for (const figure of await page.locator('.screenshot').all()) {
        await figure.scrollIntoViewIfNeeded();
        const img = figure.locator('img');
        await img.evaluate(img => img.decode());
        assert.deepEqual(await img.evaluate(img => [img.naturalWidth, img.naturalHeight]), [1720, 1124]);
        assert.equal(await figure.locator('figcaption').textContent(), '真实客户端界面，设备信息为演示数据');
      }
      const optionalDetails = page.locator('details').nth(2);
      await optionalDetails.locator('summary').click();
      assert.equal(await optionalDetails.getAttribute('open'), '');
      await optionalDetails.locator('summary').click();
      assert.equal(await optionalDetails.getAttribute('open'), null);
      await page.locator('.closing-cta a').click();
      assert.equal(new URL(page.url()).hash, '#downloads');
      const target = await page.locator('#downloads').boundingBox();
      const header = await page.locator('.site-header').boundingBox();
      assert.ok(target.y >= header.height && target.y < height, 'download anchor hidden by header');
      if (output && [1440, 390].includes(width)) {
        await page.evaluate(() => scrollTo(0, 0));
        await page.screenshot({ path: path.join(output, 'homepage-' + width + '.png'), fullPage: true });
        await page.screenshot({ path: path.join(output, 'homepage-top-' + width + '.png') });
        await page.locator('#features').evaluate(element => element.scrollIntoView());
        await page.screenshot({ path: path.join(output, 'homepage-features-' + width + '.png') });
      }
      console.log('PASS homepage viewport ' + width + 'x' + height);
      await context.close();
    }
    assert.deepEqual(failures, []);
    console.log('PASS downloads, image loading, captions, no overflow, keyboard, anchors, details, same-origin requests');
  } finally {
    await browser.close();
    await fetch(new URL('/site-preview-done', origin)).catch(() => {});
  }
})().catch(error => { console.error(error); process.exitCode = 1; });
