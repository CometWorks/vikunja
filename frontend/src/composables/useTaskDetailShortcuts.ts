import {ref, onMounted, onBeforeUnmount} from 'vue'
import {useRouter} from 'vue-router'
import {eventToShortcutString, isFormField} from '@/helpers/shortcut'

import {getTaskIdentifier, getTaskRouteId} from '@/helpers/task'
import type {Task as ITask} from '@/client/generated'

interface UseTaskDetailShortcutsOptions {
	task: () => ITask
	taskTitle: () => string
	onSave: () => void
	scrollContainer: () => HTMLElement | null
}

// Same page step as Chromium's native Page Up/Down, so consecutive pages overlap a little.
const PAGE_SCROLL_RATIO = 0.875

const SCROLL_TARGETS: Record<string, (scroller: HTMLElement) => number> = {
	PageDown: scroller => scroller.scrollTop + scroller.clientHeight * PAGE_SCROLL_RATIO,
	PageUp: scroller => scroller.scrollTop - scroller.clientHeight * PAGE_SCROLL_RATIO,
	Home: () => 0,
	'Control+Home': () => 0,
	End: scroller => scroller.scrollHeight,
	'Control+End': scroller => scroller.scrollHeight,
}

async function copySavely(value: string) {

	try {
		await navigator.clipboard.writeText(value)
	} catch(e) {
		console.error('could not write to clipboard', e)
	}
}

export function useTaskDetailShortcuts({
	task,
	taskTitle,
	onSave,
	scrollContainer,
}: UseTaskDetailShortcutsOptions) {
	const router = useRouter()

	function taskUrl() {
		const route = router.resolve({name: 'task.detail', params: {id: getTaskRouteId(task())}})
		return new URL(route.href, window.location.href).href
	}

	const dotKeyPressedTimes = ref(0)
	const dotKeyCopyValue = ref('')
	let dotKeyPressedTimeout: ReturnType<typeof setTimeout> | null = null

	function resetDotKeyPressed() {
		dotKeyPressedTimes.value = 0
		dotKeyCopyValue.value = ''
		if (dotKeyPressedTimeout !== null) {
			clearTimeout(dotKeyPressedTimeout)
			dotKeyPressedTimeout = null
		}
	}

	// See https://github.com/github/hotkey/discussions/85#discussioncomment-5214660
	async function handleTaskHotkey(event: KeyboardEvent) {
		const shortcutString = eventToShortcutString(event)
		if (!shortcutString) return

		if (shortcutString === 'Control+KeyS' || shortcutString === 'Meta+KeyS') {
			event.preventDefault()
			onSave()
			return
		}

		const target = event.target as HTMLElement

		if (isFormField(target)) {
			return
		}

		// The browser can't do this itself in the modal: focus starts on the fixed close
		// button, so native Page Down scrolls the locked viewport instead of the modal.
		const scrollTarget = SCROLL_TARGETS[shortcutString]
		const scroller = scrollContainer()
		if (scrollTarget && scroller && !event.defaultPrevented) {
			// A dialog opened over the task (delete confirmation, shortcuts help) keeps its keys.
			const dialog = target.closest('dialog')
			if (dialog && !dialog.contains(scroller)) {
				return
			}
			event.preventDefault()
			scroller.scrollTo({top: scrollTarget(scroller)})
			return
		}

		if (shortcutString === 'Control+Period') {
			await copySavely(taskUrl())
			return
		}

		if (shortcutString === 'Period') {
			dotKeyPressedTimes.value++
			if (dotKeyPressedTimeout !== null) {
				clearTimeout(dotKeyPressedTimeout)
			}
			dotKeyPressedTimeout = setTimeout(async () => {
				await copySavely(dotKeyCopyValue.value)
				resetDotKeyPressed()
			}, 300)

			switch (dotKeyPressedTimes.value) {
				case 1:
					dotKeyCopyValue.value = getTaskIdentifier(task())
					break
				case 2:
					dotKeyCopyValue.value += ' - ' + taskTitle()
					break
				case 3:
					dotKeyCopyValue.value += ' - ' + taskUrl()
					break
				default:
					resetDotKeyPressed()
			}
		}
	}

	onMounted(() => {
		document.addEventListener('keydown', handleTaskHotkey)
	})

	onBeforeUnmount(() => {
		document.removeEventListener('keydown', handleTaskHotkey)
		resetDotKeyPressed()
	})
}
