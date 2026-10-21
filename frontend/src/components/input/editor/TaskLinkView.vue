<template>
	<NodeViewWrapper
		as="span"
		class="task-link"
		:class="{'task-link--selected': selected}"
	>
		<TaskLinkPill
			:href="node.attrs.href"
			@open="open"
		/>
	</NodeViewWrapper>
</template>

<script lang="ts" setup>
import {nodeViewProps, NodeViewWrapper} from '@tiptap/vue-3'
import {useRouter} from 'vue-router'

import type {TaskResponse} from '@/client/queries/tasks'
import {openTaskInNewTab} from '@/helpers/openTaskInNewTab'
import TaskLinkPill from './TaskLinkPill.vue'

const props = defineProps(nodeViewProps)
const router = useRouter()

// Checked at click time: node views are not re-rendered when the editor
// toggles editable. In edit mode the pill only selects, like a mention.
function open(task: TaskResponse) {
	if (props.editor.isEditable) {
		return
	}
	openTaskInNewTab(router, task.id)
}
</script>

<style lang="scss">
.tiptap .task-link {
	display: inline;

	&--selected .task-link-pill--task {
		outline: 2px solid var(--primary);
		outline-offset: 1px;
	}
}
</style>
