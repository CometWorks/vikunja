import {
	useMutation,
	type QueryClient,
	type QueryKey,
} from '@tanstack/vue-query'
import {tasksRead, type TimeEntry} from '@/client/generated'
import {contextMutationOptions} from './contextMutation'
import {
	cachedTaskIdOf,
	normalizeTimeEntry,
	patchTimeEntry,
	removeTimeEntry,
	timeEntryKeys,
	type TimeEntryResponse,
} from './timeEntries'
import {notificationKeys} from './notifications'
import {commentKeys} from './comments'
import {taskKeys} from './tasks'
import {
	invalidateTaskMembership,
	removeTaskEverywhere,
	replaceTaskEverywhere,
	taskQueryKeys,
} from './taskCache'
import {placeTaskOnBoards} from './taskMutations'

export const CACHE_EVENTS = [
	'timer.created',
	'timer.updated',
	'timer.deleted',
	'notification.created',
	'task.changed',
	'comment.changed',
] as const

export type ServerCacheEventName = typeof CACHE_EVENTS[number]

type TimerEvent = 'timer.created' | 'timer.updated' | 'timer.deleted'
export type ServerCacheEvent =
	| {
		kind: TimerEvent,
		entry: TimeEntryResponse,
	}
	| {
		kind: 'notifications',
		taskId?: number,
	}
	| {
		kind: 'task' | 'comment',
		taskId: number,
	}
	| {
		kind: 'subscribed',
		since: number,
	}
	| {kind: 'reconnect'}

function isRecord(value: unknown): value is Record<string, unknown> {
	return typeof value === 'object' && value !== null
}

export function parseServerCacheEvent(
	event: ServerCacheEventName,
	data: unknown,
	currentUserId: number | undefined,
): ServerCacheEvent | null {
	if (!isRecord(data)) return null
	if (event === 'timer.created' || event === 'timer.updated' || event === 'timer.deleted') {
		if (currentUserId === undefined || data.user_id !== currentUserId) return null
		if (typeof data.id !== 'number' || data.id <= 0) return null
		if (data.task_id !== undefined && data.task_id !== null && typeof data.task_id !== 'number') return null
		return {
			kind: event,
			entry: normalizeTimeEntry(data as TimeEntry),
		}
	}
	if (event === 'task.changed' || event === 'comment.changed') {
		if (typeof data.task_id !== 'number' || data.task_id <= 0) return null
		return {
			kind: event === 'task.changed' ? 'task' : 'comment',
			taskId: data.task_id,
		}
	}
	if (event === 'notification.created') {
		const task = data.name === 'task.comment' && isRecord(data.notification) ? data.notification.task : undefined
		const taskId = isRecord(task) && typeof task.id === 'number' && task.id > 0 ? task.id : undefined
		if (taskId === undefined && !(typeof data.id === 'number' && data.id > 0)) return null
		return {
			kind: 'notifications',
			taskId,
		}
	}
	return null
}

const startsWith = (key: QueryKey, prefix: readonly unknown[]) => prefix.every((part, index) => key[index] === part)
const isTaskDetailKey = (key: QueryKey) => startsWith(key, taskKeys.details)

const SUBSCRIBE_SWEEP_KEYS = [
	notificationKeys.all,
	timeEntryKeys.all,
	commentKeys.all,
	taskKeys.details,
]

// Only collections that already hold the task can have gone stale; its own detail key is invalidated anyway.
async function invalidateCachedTask(client: QueryClient, taskId: number | undefined): Promise<void> {
	if (!taskId) return
	const heldByCollection = taskQueryKeys(client, taskId).some(key => !isTaskDetailKey(key))
	await Promise.all([
		client.invalidateQueries({queryKey: taskKeys.detail(taskId)}),
		...(heldByCollection ? [invalidateTaskMembership(client)] : []),
	])
}

// Someone changed a task: patch the fresh copy into every cached view. Boards are
// patched instead of refetched, a refetch would drop the bucket pages loaded so far.
async function applyRemoteTaskChange(client: QueryClient, taskId: number): Promise<void> {
	try {
		const task = (await tasksRead({path: {task: taskId}, query: {expand: ['buckets']}})).data
		replaceTaskEverywhere(client, task)
		placeTaskOnBoards(client, task)
	} catch (cause) {
		const status = (cause as {status?: number} | null)?.status
		if (status !== 403 && status !== 404) throw cause
		// Deleted, or moved somewhere this user cannot read.
		removeTaskEverywhere(client, taskId)
	}
	await Promise.all([
		client.invalidateQueries({queryKey: [...taskKeys.details, taskId]}),
		// Filters and sorting decide list membership, only the server knows the new order.
		client.invalidateQueries({queryKey: taskKeys.lists}),
		client.invalidateQueries({queryKey: taskKeys.allLists}),
	])
}

export function serverCacheEventMutationOptions() {
	return contextMutationOptions({
		mutationFn: async (event: ServerCacheEvent) => event,
		onSettled: (event, client) => {
			if ('entry' in event) {
				// The payload carries only the new task, so a reassignment would leave the old one's count stale.
				const previousTaskId = event.kind === 'timer.updated' ? cachedTaskIdOf(client, event.entry.id) : undefined
				if (event.kind === 'timer.deleted') removeTimeEntry(client, event.entry.id)
				else patchTimeEntry(client, event.entry)
				return Promise.all([
					// Whether a new entry belongs to a server-filtered list cannot be decided from the payload.
					...(event.kind === 'timer.created' ? [client.invalidateQueries({queryKey: timeEntryKeys.lists})] : []),
					invalidateCachedTask(client, event.entry.task_id),
					...(previousTaskId === event.entry.task_id ? [] : [invalidateCachedTask(client, previousTaskId)]),
				])
			}
			if (event.kind === 'task') return applyRemoteTaskChange(client, event.taskId)
			if (event.kind === 'comment') {
				return Promise.all([
					client.invalidateQueries({queryKey: commentKeys.task(event.taskId)}),
					// The comment count lives on the task.
					client.invalidateQueries({queryKey: [...taskKeys.details, event.taskId]}),
				])
			}
			if (event.kind === 'notifications') {
				const taskId = event.taskId
				return Promise.all([
					client.invalidateQueries({queryKey: notificationKeys.all}),
					...(taskId === undefined ? [] : [
						client.invalidateQueries({queryKey: commentKeys.task(taskId)}),
						invalidateCachedTask(client, taskId),
					]),
				])
			}
			if (event.kind === 'subscribed') {
				// Data that arrived before the subscribe frames went out may have missed an event nobody saw.
				return client.invalidateQueries({
					predicate: query => query.state.dataUpdatedAt > 0
						&& query.state.dataUpdatedAt < event.since
						&& SUBSCRIBE_SWEEP_KEYS.some(prefix => startsWith(query.queryKey, prefix)),
				})
			}
			return Promise.all([
				client.invalidateQueries({queryKey: timeEntryKeys.all}),
				client.invalidateQueries({queryKey: notificationKeys.all}),
				client.invalidateQueries({queryKey: commentKeys.all}),
				client.invalidateQueries({queryKey: taskKeys.details}),
				invalidateTaskMembership(client),
			])
		},
		toastError: () => false,
	})
}

export function useServerCacheEventMutation() {
	return useMutation(serverCacheEventMutationOptions())
}
