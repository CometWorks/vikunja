<template>
	<Modal
		:enabled="enabled"
		:aria-label="$t('task.detail.conflict.title')"
		@close="$emit('cancel')"
	>
		<div class="modal-header">
			{{ $t('task.detail.conflict.title') }}
		</div>
		<div class="content">
			<p>{{ $t('task.detail.conflict.text', {field}) }}</p>
			<h3>{{ $t('task.detail.conflict.theirs') }}</h3>
			<blockquote class="conflict-version">
				{{ plainText(theirs) }}
			</blockquote>
			<h3>{{ $t('task.detail.conflict.mine') }}</h3>
			<blockquote class="conflict-version">
				{{ plainText(mine) }}
			</blockquote>
		</div>
		<div class="actions">
			<XButton
				variant="tertiary"
				@click="$emit('useTheirs')"
			>
				{{ $t('task.detail.conflict.useTheirs') }}
			</XButton>
			<XButton
				variant="primary"
				:shadow="false"
				@click="$emit('keepMine')"
			>
				{{ $t('task.detail.conflict.keepMine') }}
			</XButton>
		</div>
	</Modal>
</template>

<script setup lang="ts">
import Modal from '@/components/misc/Modal.vue'

defineProps<{
	enabled: boolean,
	field: string,
	theirs: string,
	mine: string,
}>()

defineEmits<{
	keepMine: [],
	useTheirs: [],
	cancel: [],
}>()

// Descriptions are HTML, the comparison only needs the words.
function plainText(html: string): string {
	return new DOMParser().parseFromString(html, 'text/html').body.textContent?.trim() ?? ''
}
</script>

<style lang="scss" scoped>
.conflict-version {
	max-block-size: 10rem;
	overflow-y: auto;
	white-space: pre-wrap;
}
</style>
