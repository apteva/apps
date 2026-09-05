import { test, expect } from '@playwright/test';

test('release modules mount against the production host import map', async ({ page }) => {
  const errors: string[] = [];
  page.on('pageerror', error => { errors.push(error.message); console.error(error.message); });
  await page.route('**/api/apps/storage/**', async route => {
    const path = new URL(route.request().url()).pathname;
    if (path.includes('/ui/')) return route.continue();
    if (path.endsWith('/folders')) return route.fulfill({ json: { folders: [] } });
    return route.fulfill({ json: { files: [] } });
  });
  await page.goto('/');
  await expect(page.getByRole('button', { name: '+ Folder', exact: true })).toBeVisible();
  expect(errors).toEqual([]);
  const exports = await page.evaluate(async () => Object.keys(await import('/vendor/react-jsx-runtime.mjs')));
  expect(exports).toContain('jsx');
  expect(exports).not.toContain('jsxDEV');
});
