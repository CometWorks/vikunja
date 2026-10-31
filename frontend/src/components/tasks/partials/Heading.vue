<template>
	<div class="heading">
		<div class="tw:flex tw:items-center md:tw:items-stretch tw:flex-col tw:gap-1 task-properties">
			<div class="tw:flex tw:items-center tw:gap-2">
				<ColorBubble
					v-if="task.hex_color !== ''"
					:color="getHexColor(task.hex_color)"
				/>
				<BaseButton
					v-tooltip="$t('keyboardShortcuts.task.copyIdentifier')"
					@click="copyIdentifier"
				>
					<span class="title task-id">
						{{ textIdentifier }}
					</span>
				</BaseButton>
				<BaseButton
					v-tooltip="$t('keyboardShortcuts.task.copyUrl')"
					:aria-label="$t('keyboardShortcuts.task.copyUrl')"
					class="copy-link d-print-none"
					@click="copyUrl"
				>
					<Icon icon="link" />
				</BaseButton>
			</div>
			<Done
				:is-done="task.done"
			/>
			<BaseButton
				v-if="hasClose"
				:aria-label="$t('task.detail.closeTaskDetail')"
				class="close d-print-none"
				@click="$emit('close')"
			>
				<Icon icon="times" />
			</BaseButton>
		</div>
		<h1
			class="title input"
			:class="{'disabled': !canWrite}"
			:contenteditable="canWrite ? true : undefined"
			:tabindex="canWrite ? 0 : undefined"
			:aria-label="canWrite ? $t('task.attributes.title') : undefined"
			:spellcheck="false"
			@input="handleTitleInput"
			@blur="save($event.target as HTMLElement)"
			@keydown.enter.prevent.stop="!$event.isComposing && ($event.target as HTMLInputElement).blur()"
			@keydown.esc.prevent.stop="!$event.isComposing && cancel($event.target as HTMLInputElement)"
		>
			{{ shownTitle }}
		</h1>
		<EditConflictModal
			v-if="conflictElement !== null"
			:enabled="true"
			:field="$t('task.attributes.title')"
			:theirs="task.title ?? ''"
			:mine="conflictElement?.textContent ?? ''"
			@keepMine="keepMine"
			@useTheirs="useTheirs"
			@cancel="conflictElement = null"
		/>
		<CustomTransition name="fade">
			<span
				v-if="loading && saving"
				class="is-inline-flex is-align-items-center"
			>
				<span class="loader is-inline-block mie-2" />
				{{ $t('misc.saving') }}
			</span>
			<span
				v-else-if="!loading && showSavedMessage"
				class="has-text-success is-inline-flex is-align-content-center"
			>
				<Icon
					icon="check"
					class="mie-2"
				/>
				{{ $t('misc.saved') }}
			</span>
		</CustomTransition>
	</div>
</template>

<script setup lang="ts">
import {ref, computed, onMounted, onBeforeUnmount, watch} from 'vue'
import {useRouter} from 'vue-router'
import {useI18n} from 'vue-i18n'

import {error, success} from '@/message'
import BaseButton from '@/components/base/BaseButton.vue'
import CustomTransition from '@/components/misc/CustomTransition.vue'
import ColorBubble from '@/components/misc/ColorBubble.vue'
import Done from '@/components/misc/Done.vue'
import EditConflictModal from '@/components/tasks/partials/EditConflictModal.vue'

import {useCopyToClipboard} from '@/composables/useCopyToClipboard'
import {useDelayedLoading} from '@/composables/useDelayedLoading'
import {useUpdateTaskMutation} from '@/client/queries/taskMutations'

import type {Task as ITask} from '@/client/generated'
import {getHexColor, getTaskIdentifier, getTaskUrl} from '@/helpers/task'

const props = defineProps<{
	task: ITask,
	canWrite: boolean,
	hasClose: boolean,
}>()

const emit = defineEmits<{
	'update:task': [task: ITask],
	'close': [],
}>()

const router = useRouter()
const copy = useCopyToClipboard()
const {t} = useI18n({useScope: 'global'})

async function copyIdentifier() {
	await copy(textIdentifier.value)
	success({message: t('task.detail.identifierCopied', {identifier: textIdentifier.value})})
}

async function copyUrl() {
	await copy(getTaskUrl(router, props.task))
	success({message: t('task.detail.linkCopied')})
}

const updateTask = useUpdateTaskMutation()
const loading = useDelayedLoading(updateTask.isPending)

const textIdentifier = computed(() => getTaskIdentifier(props.task))

// Since loading is global state, this variable ensures we're only showing the saving icon when saving the description.
const saving = ref(false)

const showSavedMessage = ref(false)

// Track if title has unsaved changes
const titleHasChanges = ref(false)

function handleBeforeUnload(e: BeforeUnloadEvent) {
	if (titleHasChanges.value) {
		e.preventDefault()
		// Modern browsers ignore custom messages but this is still required
		e.returnValue = ''
		return ''
	}
}

onMounted(() => {
	window.addEventListener('beforeunload', handleBeforeUnload)
})

onBeforeUnmount(() => {
	window.removeEventListener('beforeunload', handleBeforeUnload)
})

// Reset titleHasChanges when the task changes
watch(() => props.task.id, () => {
	titleHasChanges.value = false
})

// The title shown while editing stays as typed. Changes streamed in meanwhile
// only move the server's title, saving then asks which version to keep.
const shownTitle = ref('')
const baseTitle = ref('')
const conflictElement = ref<HTMLElement | null>(null)
watch(
	() => [props.task.id, props.task.title] as const,
	([id, title], previous) => {
		if (id === previous?.[0] && titleHasChanges.value) return
		shownTitle.value = (title ?? '').trim()
		baseTitle.value = title ?? ''
	},
	{immediate: true},
)

function handleTitleInput(event: Event) {
	const target = event.target as HTMLInputElement
	titleHasChanges.value = target.textContent !== (props.task.title ?? '')
}

async function save(element: HTMLElement) {
	const title = element.textContent ?? ''

	// An empty title would be discarded by the api, so revert and tell the user instead of failing silently.
	if (title.trim() === '') {
		element.textContent = props.task.title ?? ''
		titleHasChanges.value = false
		error({message: t('task.detail.titleRequired')})
		return
	}

	// We only want to save if the title was actually changed.
	// so we only continue if the task title changed.
	if (title === props.task.title) {
		titleHasChanges.value = false
		return
	}

	if (props.task.title !== baseTitle.value) {
		conflictElement.value = element
		return
	}

	try {
		saving.value = true
		const newTask = await updateTask.mutateAsync({
			...props.task,
			id: props.task.id!,
			title,
		})
		emit('update:task', newTask)
		titleHasChanges.value = false
		baseTitle.value = newTask.title ?? title
		showSavedMessage.value = true
		setTimeout(() => {
			showSavedMessage.value = false
		}, 2000)
	} finally {
		saving.value = false
	}
}

async function keepMine() {
	const element = conflictElement.value
	conflictElement.value = null
	if (!element) return
	baseTitle.value = props.task.title ?? ''
	await save(element)
}

function useTheirs() {
	const element = conflictElement.value
	conflictElement.value = null
	if (element) element.textContent = props.task.title ?? ''
	shownTitle.value = (props.task.title ?? '').trim()
	baseTitle.value = props.task.title ?? ''
	titleHasChanges.value = false
}

async function cancel(element: HTMLInputElement) {
	element.textContent = props.task.title ?? ''
	titleHasChanges.value = false
	element.blur()
}
</script>

<style lang="scss" scoped>
.heading {
	display: flex;
	justify-content: flex-start;
	text-transform: none;
	align-items: center;

	@media screen and (max-width: $tablet) {
		flex-direction: column;
		align-items: start;
	}
}

.title {
	margin-block-end: 0;
}

.title.input {
	// 1.8rem is the font-size, 1.125 is the line-height, .3rem padding everywhere, 1px border around the whole thing.
	min-block-size: calc(1.8rem * 1.125 + .6rem + 2px);
	margin-inline-end: 0;

	@media screen and (max-width: $tablet) {
		margin: 0 -.3rem .5rem; // the title has 0.3rem padding - this make the text inside of it align with the rest
	}
}

.title.task-id {
	color: var(--grey-400);
	white-space: nowrap;
}

.copy-link {
	color: var(--grey-400);
}

.color-bubble {
	block-size: .75rem;
	inline-size: .75rem;
}

// Modal renders its own fixed close button from $tablet up
.close {
	display: none;
	font-size: 2rem;
	line-height: 1;
	position: absolute;
	inset-inline-end: 1.25rem;
	inset-block-start: 1.1rem;

	@media screen and (max-width: $tablet) {
		display: block;
	}
}

.task-properties {
	@media screen and (max-width: $tablet) {
		flex-direction: row;
	}
}
</style>
