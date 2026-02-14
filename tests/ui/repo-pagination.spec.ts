import { test, expect } from '@playwright/test';

test('project pagination and sorting', async ({ page }) => {
  await page.goto('/projects/project?per=25');

  const rows = page.locator('.stack-row');
  await expect(rows).toHaveCount(25);

  const firstPathAsc = await page.locator('.stack-row .stack-link').first().innerText();

  await page.selectOption('select[name="sort"]', 'path');
  await page.selectOption('select[name="order"]', 'desc');
  await page.click('text=Apply');

  const firstPathDesc = await page.locator('.stack-row .stack-link').first().innerText();
  expect(firstPathDesc).not.toEqual(firstPathAsc);
});
