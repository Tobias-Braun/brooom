<script setup lang="ts">
/**
 * Copy-to-clipboard button. The clipboard logic lives in lib/clipboard.ts;
 * this component only owns the visual state, the polite live region that
 * announces it (changing the button's text alone is not reliably read out)
 * and the timer that reverts the state, which is cleared on unmount.
 */
import { computed, onBeforeUnmount, ref } from 'vue';
import { copyText, type CopyResult } from '../lib/clipboard';

const props = withDefaults(
  defineProps<{
    /** Exactly what lands on the clipboard: the visible code, no prompt characters. */
    text: string;
    /** Accessible name of the button. */
    label?: string;
    /** Extra classes for the button element. */
    class?: string;
  }>(),
  { label: 'Copy', class: '' },
);

const REVERT_MS = 2000;
// Manual-copy hints stay longer: the visitor still has to press the keys.
const HINT_REVERT_MS = 5000;

const result = ref<CopyResult | null>(null);
let timer: number | null = null;

const visibleText = computed(() => (result.value === 'copied' ? 'Copied' : 'Copy'));
const status = computed(() => {
  switch (result.value) {
    case 'copied':
      return 'Copied to clipboard';
    case 'fallback':
      return 'Press Ctrl/Cmd+C to copy';
    case 'failed':
      return 'Copy failed. Select the text and press Ctrl/Cmd+C';
    default:
      return '';
  }
});

function clearTimer() {
  if (timer !== null) window.clearTimeout(timer);
  timer = null;
}

async function onClick() {
  // Called synchronously from the click so Safari still sees a user gesture.
  const outcome = await copyText(props.text);
  clearTimer();
  result.value = outcome;
  timer = window.setTimeout(
    () => {
      result.value = null;
      timer = null;
    },
    outcome === 'copied' ? REVERT_MS : HINT_REVERT_MS,
  );
}

onBeforeUnmount(clearTimer);
</script>

<template>
  <span class="copy">
    <button
      type="button"
      class="btn-copy"
      :class="[props.class, { done: result === 'copied' }]"
      :aria-label="props.label"
      @click="onClick"
    >
      <svg v-if="result === 'copied'" viewBox="0 0 16 16" width="16" height="16" aria-hidden="true" focusable="false">
        <path d="M3 8.5l3.2 3.2L13 4.8" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" />
      </svg>
      <svg v-else viewBox="0 0 16 16" width="16" height="16" aria-hidden="true" focusable="false">
        <path d="M5.5 5.5h7v8h-7zM3.5 10.5v-8h7" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linejoin="round" />
      </svg>
      <span aria-hidden="true">{{ visibleText }}</span>
    </button>
    <span v-if="result === 'fallback' || result === 'failed'" class="hint" aria-hidden="true">{{ status }}</span>
    <span class="visually-hidden" role="status" aria-live="polite">{{ status }}</span>
  </span>
</template>

<style scoped>
.copy {
  display: inline-flex;
  align-items: center;
  flex-wrap: wrap;
  gap: var(--space-2);
}

.btn-copy {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: var(--space-2);
  min-width: 44px;
  min-height: 44px;
  padding: 0 var(--space-3);
  border: 1px solid var(--border);
  border-radius: var(--radius-sm);
  background: var(--surface);
  color: var(--text);
  font: inherit;
  font-size: 0.9rem;
  cursor: pointer;
}

.btn-copy:hover {
  border-color: var(--violet);
}

.btn-copy.done {
  color: var(--neon);
  border-color: var(--neon);
}

.hint {
  color: var(--text-muted);
  font-size: 0.85rem;
}
</style>
