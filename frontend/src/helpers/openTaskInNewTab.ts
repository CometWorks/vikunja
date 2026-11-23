import type {Router} from 'vue-router'
import type {Task} from '@/client/generated'
import {getTaskRoute} from '@/helpers/task'

// Existing tasks open as a full page in a new tab instead of the detail modal,
// which kept long tasks from scrolling with the keyboard.
export function openTaskInNewTab(router: Router, task: Pick<Task, 'id' | 'identifier'>) {
	window.open(router.resolve(getTaskRoute(task)).href, '_blank', 'noopener')
}
