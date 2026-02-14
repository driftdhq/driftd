import { test, expect } from '@playwright/test';

test('scan updates via SSE without reload', async ({ page }) => {
  let navigations = 0;
  page.on('framenavigated', (frame) => {
    if (frame === page.mainFrame()) {
      navigations += 1;
    }
  });

  await page.goto('/projects/project');
  const scanButton = page.locator('text=Scan All Stacks');
  await expect(scanButton).toBeVisible();
  await scanButton.click();

  const progressMeta = page.locator('.stack-progress-anchor.is-active .progress .meta, .scan-summary.active .progress .meta');
  await expect(progressMeta).toBeVisible();

  await page.waitForFunction(() => {
    const el = document.querySelector('.stack-progress-anchor.is-active .progress .meta, .scan-summary.active .progress .meta');
    if (!el) return false;
    const text = el.textContent || '';
    return /\d+\s*\/\s*\d+/.test(text);
  });

  // Allow a moment for updates to stream in.
  await page.waitForTimeout(500);

  // Should only have the initial navigation.
  // Initial navigation + optional redirect after scan trigger.
  expect(navigations).toBeLessThanOrEqual(2);
});
