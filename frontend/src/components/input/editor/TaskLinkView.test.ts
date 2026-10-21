import {describe, it, expect, beforeEach, vi} from 'vitest'
import {mount} from '@vue/test-utils'

vi.mock('vue-router', async (importOriginal) => ({
	...await importOriginal<typeof import('vue-router')>(),
	useRouter: () => ({resolve: ({params}: {params: {id: number}}) => ({href: `/tasks/${params.id}`})}),
}))

import TaskLinkView from './TaskLinkView.vue'

const HREF = 'http://localhost:3000/tasks/5'

function mountView(isEditable: boolean) {
	return mount(TaskLinkView, {
		props: {
			node: {attrs: {href: HREF}},
			editor: {isEditable},
			decorations: [],
			selected: false,
			extension: {},
			getPos: () => 0,
			updateAttributes: () => {},
			deleteNode: () => {},
			view: {},
			innerDecorations: {},
			HTMLAttributes: {},
		} as never,
		global: {
			stubs: {
				NodeViewWrapper: {template: '<span><slot /></span>'},
				TaskLinkPill: {
					template: '<a @click="$emit(\'open\', {id: 5})" />',
					emits: ['open'],
				},
			},
		},
	})
}

describe('TaskLinkView', () => {
	const open = vi.fn()

	beforeEach(() => {
		open.mockReset()
		vi.stubGlobal('open', open)
	})

	it('opens the task in a new tab when not editable', async () => {
		const wrapper = mountView(false)

		await wrapper.find('a').trigger('click')

		expect(open).toHaveBeenCalledWith('/tasks/5', '_blank', 'noopener')
	})

	it('does not navigate while the editor is editable', async () => {
		const wrapper = mountView(true)

		await wrapper.find('a').trigger('click')

		expect(open).not.toHaveBeenCalled()
	})
})
