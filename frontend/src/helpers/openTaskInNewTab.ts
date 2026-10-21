import type {Router} from 'vue-router'

// Existing tasks open as a full page in a new tab instead of the detail modal,
// which kept long tasks from scrolling with the keyboard.
export function openTaskInNewTab(router: Router, id: number | string) {
	window.open(router.resolve({name: 'task.detail', params: {id}}).href, '_blank', 'noopener')
}
