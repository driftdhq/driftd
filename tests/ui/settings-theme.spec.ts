import { test, expect } from '@playwright/test';

test('settings tabs and theme switching', async ({ page }) => {
  await page.goto('/settings');

  await page.click('button[data-tab="appearance"]');
  const html = page.locator('html');

  await page.click('button.theme-btn[data-theme="light"]');
  await expect(html).toHaveAttribute('data-theme', 'light');

  await page.click('button.theme-btn[data-theme="dark"]');
  await expect(html).toHaveAttribute('data-theme', 'dark');
});
