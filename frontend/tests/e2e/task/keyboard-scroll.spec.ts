import type {Page} from '@playwright/test'

import {test, expect} from '../../support/fixtures'
import {ProjectFactory} from '../../factories/project'
import {TaskFactory} from '../../factories/task'
import {TaskCommentFactory} from '../../factories/task_comment'
import {BucketFactory} from '../../factories/bucket'
import {TaskBucketFactory} from '../../factories/task_buckets'
import {createDefaultViews} from '../project/prepareProjects'

const COMMENT_COUNT = 20
const longDescription = Array.from(
	{length: 40},
	(_, i) => `<p>Paragraph ${i + 1} of a long task description.</p>`,
).join('')

const contexts = [
	{
		name: 'task page',
		inModal: false,
		open: async (page: Page) => page.goto('/tasks/1'),
	},
	{
		name: 'task modal on the kanban board',
		inModal: true,
		open: async (page: Page) => {
			await page.goto('/projects/1/4')
			await page.locator('.bucket .task').first().click()
		},
	},
]

function scrollState(page: Page, inModal: boolean) {
	return page.evaluate((modal) => {
		const scroller = modal
			? document.querySelector('dialog[open] .modal-container')!
			: document.scrollingElement!
		return {
			top: scroller.scrollTop,
			pageHeight: scroller.clientHeight,
			atBottom: scroller.scrollTop + scroller.clientHeight >= scroller.scrollHeight - 1,
		}
	}, inModal)
}

test.describe('Task detail keyboard scrolling', () => {
	test.beforeEach(async () => {
		await ProjectFactory.create(1)
		const views = await createDefaultViews(1)
		const buckets = await BucketFactory.create(1, {project_view_id: views[3].id})
		await TaskFactory.create(1, {
			id: 1,
			description: longDescription,
		})
		await TaskBucketFactory.create(1, {
			task_id: 1,
			bucket_id: buckets[0].id,
			project_view_id: views[3].id,
		})
		await TaskCommentFactory.create(COMMENT_COUNT)
	})

	for (const {name, inModal, open} of contexts) {
		test.describe(name, () => {
			test.beforeEach(async ({authenticatedPage: page}) => {
				await open(page)
				await expect(page.locator('.task-view .comments .comment[id^="comment-"]')).toHaveCount(COMMENT_COUNT)
				await expect.poll(async () => (await scrollState(page, inModal)).top).toBe(0)
			})

			test('Page Up/Down scroll by a page, Ctrl+End and Ctrl+Home jump to the ends', async ({authenticatedPage: page}) => {
				await page.keyboard.press('PageDown')
				await expect.poll(async () => (await scrollState(page, inModal)).top).toBeGreaterThan(0)
				const afterPageDown = await scrollState(page, inModal)
				expect(afterPageDown.top).toBeLessThan(afterPageDown.pageHeight)

				await page.keyboard.press('PageUp')
				await expect.poll(async () => (await scrollState(page, inModal)).top).toBe(0)

				await page.keyboard.press('Control+End')
				await expect.poll(async () => (await scrollState(page, inModal)).atBottom).toBe(true)

				await page.keyboard.press('Control+Home')
				await expect.poll(async () => (await scrollState(page, inModal)).top).toBe(0)
			})

			test('the keys keep their editing behavior in the comment editor', async ({authenticatedPage: page}) => {
				const editor = page.locator('.comments .tiptap[contenteditable="true"]').last()
				await editor.click()
				await page.keyboard.type('abc')
				const before = await scrollState(page, inModal)
				expect(before.top).toBeGreaterThan(0)

				await page.evaluate(() => {
					window.addEventListener('keydown', e => {
						if (e.defaultPrevented) {
							document.body.dataset.preventedKey = e.key
						}
					})
				})
				await page.keyboard.press('Control+Home')
				await page.keyboard.press('PageUp')
				await page.keyboard.press('Control+Home')
				await page.keyboard.type('X')

				await expect(editor).toHaveText('Xabc')
				await expect(page.locator('body')).not.toHaveAttribute('data-prevented-key')
				expect((await scrollState(page, inModal)).top).toBeGreaterThan(0)
			})

			test('the jump-to-bottom button shows until the end of the task is reached', async ({authenticatedPage: page}) => {
				const button = page.getByRole('button', {name: 'Scroll to bottom'})
				await expect(button).toBeVisible()

				await button.click()

				await expect(page.locator('.comments').getByRole('button', {name: 'Comment', exact: true})).toBeInViewport()
				await expect(button).toBeHidden()
			})
		})
	}
})
