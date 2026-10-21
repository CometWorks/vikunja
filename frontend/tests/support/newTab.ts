import {expect, type Page} from '@playwright/test'

/**
 * Runs `open`, which should open the task in a new tab, and returns that tab.
 * Also checks the original page stayed put without a task modal.
 */
export async function expectTaskInNewTab(page: Page, taskId: number, open: () => Promise<void>) {
	const urlBefore = page.url()
	const [taskPage] = await Promise.all([
		page.context().waitForEvent('page'),
		open(),
	])
	await expect(taskPage).toHaveURL(new RegExp(`/tasks/${taskId}$`))
	await expect(taskPage.locator('.task-view')).toBeVisible()
	await expect(page.locator('dialog[open]')).toHaveCount(0)
	expect(page.url()).toBe(urlBefore)
	return taskPage
}
