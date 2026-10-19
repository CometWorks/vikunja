import type {APIRequestContext, Page} from '@playwright/test'
import {
	test,
	expect,
} from '../../support/fixtures'
import {BucketFactory} from '../../factories/bucket'
import {ProjectFactory} from '../../factories/project'
import {ProjectViewFactory} from '../../factories/project_view'
import {TaskFactory} from '../../factories/task'
import {TaskBucketFactory} from '../../factories/task_buckets'
import {TaskCommentFactory} from '../../factories/task_comment'

// Subscriptions go out in one batch after auth, so the last one proves all of them arrived.
const LAST_SUBSCRIBED_EVENT = 'comment.changed'

async function openSubscribed(page: Page, url: string) {
	const subscribed = page.waitForEvent('websocket').then(socket => socket.waitForEvent('framesent', {
		predicate: frame => {
			const message = JSON.parse(frame.payload.toString())
			return message.action === 'subscribe' && message.event === LAST_SUBSCRIBED_EVENT
		},
	}))
	await page.goto(url)
	await subscribed
}

// Another client of the same account, talking to the API directly.
function remote(apiContext: APIRequestContext, token: string) {
	const headers = {Authorization: `Bearer ${token}`}
	return {
		patchTask: async (id: number, fields: Record<string, unknown>) => {
			const response = await apiContext.patch(`/api/v2/tasks/${id}`, {
				headers: {...headers, 'Content-Type': 'application/merge-patch+json'},
				data: fields,
			})
			expect(response.ok()).toBeTruthy()
		},
		getTask: async (id: number) => (await apiContext.get(`/api/v2/tasks/${id}`, {headers})).json(),
		headers,
	}
}

async function createBoard() {
	const [project] = await ProjectFactory.create(1)
	const [view] = await ProjectViewFactory.create(1, {
		id: 1,
		project_id: project.id,
		view_kind: 3,
		bucket_configuration_mode: 1,
	})
	const buckets = await BucketFactory.create(2, {project_view_id: view.id})
	const [task] = await TaskFactory.create(1, {project_id: project.id, title: 'Streamed card'})
	await TaskBucketFactory.create(1, {
		task_id: task.id,
		bucket_id: buckets[0].id,
		project_view_id: view.id,
	})
	return {project, view, buckets, task}
}

test('changes made elsewhere show up on the open board', async ({authenticatedPage: page, apiContext, userToken}) => {
	const {project, view, buckets, task} = await createBoard()
	const api = remote(apiContext, userToken)
	await openSubscribed(page, `/projects/${project.id}/${view.id}`)
	const firstBucket = page.locator('.kanban .bucket').filter({hasText: buckets[0].title})
	const secondBucket = page.locator('.kanban .bucket').filter({hasText: buckets[1].title})
	await expect(firstBucket).toContainText('Streamed card')

	await api.patchTask(task.id, {title: 'Renamed remotely'})
	await expect(firstBucket).toContainText('Renamed remotely')

	const moved = await apiContext.put(`/api/v2/projects/${project.id}/views/${view.id}/buckets/${buckets[1].id}/tasks`, {
		headers: api.headers,
		data: {task_id: task.id},
	})
	expect(moved.ok()).toBeTruthy()
	await expect(secondBucket).toContainText('Renamed remotely')
	await expect(firstBucket).not.toContainText('Renamed remotely')

	const created = await apiContext.post(`/api/v2/projects/${project.id}/tasks`, {
		headers: api.headers,
		data: {title: 'Created remotely'},
	})
	expect(created.ok()).toBeTruthy()
	await expect(page.locator('.kanban')).toContainText('Created remotely')

	await page.reload()
	await expect(secondBucket).toContainText('Renamed remotely')
	await expect(page.locator('.kanban')).toContainText('Created remotely')
})

test('changes made elsewhere show up in the open list', async ({authenticatedPage: page, apiContext, userToken}) => {
	const [project] = await ProjectFactory.create(1)
	const [view] = await ProjectViewFactory.create(1, {id: 1, project_id: project.id, view_kind: 0})
	const [task] = await TaskFactory.create(1, {project_id: project.id, title: 'Listed task'})
	const api = remote(apiContext, userToken)
	await openSubscribed(page, `/projects/${project.id}/${view.id}`)
	await expect(page.locator('.tasks')).toContainText('Listed task')

	await api.patchTask(task.id, {title: 'Listed and renamed'})
	await expect(page.locator('.tasks')).toContainText('Listed and renamed')
})

test('comments written elsewhere show up in the open task', async ({authenticatedPage: page, apiContext, userToken}) => {
	await ProjectFactory.create(1)
	const [task] = await TaskFactory.create(1, {project_id: 1})
	await TaskCommentFactory.truncate()
	await openSubscribed(page, `/tasks/${task.id}`)

	const response = await apiContext.post(`/api/v2/tasks/${task.id}/comments`, {
		headers: {Authorization: `Bearer ${userToken}`},
		data: {comment: '<p>Streamed comment</p>'},
	})
	expect(response.ok()).toBeTruthy()
	await expect(page.locator('.comments .comment').filter({hasText: 'Streamed comment'})).toBeVisible()
})

test.describe('editing while someone else changes the task', () => {
	async function editDescription(page: Page, text: string) {
		await page.locator('.task-view .details.content.description .tiptap button.done-edit').click()
		const editor = page.locator('.task-view .details.content.description .tiptap__editor .tiptap.ProseMirror')
		await editor.fill(text)
		return editor
	}

	test('keeps the local description and lets the user keep it on save', async ({authenticatedPage: page, apiContext, userToken}) => {
		await ProjectFactory.create(1)
		const [task] = await TaskFactory.create(1, {project_id: 1, description: '<p>Original</p>'})
		const api = remote(apiContext, userToken)
		await openSubscribed(page, `/tasks/${task.id}`)
		const editor = await editDescription(page, 'Mine')

		await api.patchTask(task.id, {description: '<p>Theirs</p>'})
		// Give the streamed change time to arrive; it must not replace the edit.
		await page.waitForTimeout(1000)
		await expect(editor).toHaveText('Mine')

		await page.locator('[data-cy="saveEditor"]').filter({hasText: 'Save'}).click()
		const dialog = page.getByRole('dialog', {name: 'Changed by someone else'})
		await expect(dialog).toContainText('Theirs')
		await expect(dialog).toContainText('Mine')
		await dialog.getByRole('button', {name: 'Keep my version'}).click()

		await expect.poll(async () => (await api.getTask(task.id)).description).toContain('Mine')
	})

	test('can take the other description instead', async ({authenticatedPage: page, apiContext, userToken}) => {
		await ProjectFactory.create(1)
		const [task] = await TaskFactory.create(1, {project_id: 1, description: '<p>Original</p>'})
		const api = remote(apiContext, userToken)
		await openSubscribed(page, `/tasks/${task.id}`)
		await editDescription(page, 'Mine')
		await api.patchTask(task.id, {description: '<p>Theirs</p>'})
		await page.waitForTimeout(1000)

		await page.locator('[data-cy="saveEditor"]').filter({hasText: 'Save'}).click()
		await page.getByRole('dialog', {name: 'Changed by someone else'})
			.getByRole('button', {name: 'Use their version'}).click()

		await expect(page.locator('.task-view .details.content.description')).toContainText('Theirs')
		await page.reload()
		await expect(page.locator('.task-view .details.content.description')).toContainText('Theirs')
		expect((await api.getTask(task.id)).description).toContain('Theirs')
	})

	test('asks before saving over a title changed meanwhile', async ({authenticatedPage: page, apiContext, userToken}) => {
		await ProjectFactory.create(1)
		const [task] = await TaskFactory.create(1, {project_id: 1, title: 'Original title'})
		const api = remote(apiContext, userToken)
		await openSubscribed(page, `/tasks/${task.id}`)
		const title = page.locator('.task-view h1.title.input')
		await title.click()
		await title.fill('My title')

		await api.patchTask(task.id, {title: 'Their title'})
		await page.waitForTimeout(1000)
		await expect(title).toHaveText('My title')

		await title.press('Enter')
		await page.getByRole('dialog', {name: 'Changed by someone else'})
			.getByRole('button', {name: 'Keep my version'}).click()

		await expect.poll(async () => (await api.getTask(task.id)).title).toBe('My title')
		await page.reload()
		await expect(title).toHaveText('My title')
	})
})
