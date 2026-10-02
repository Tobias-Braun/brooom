<script setup lang="ts">
/**
 * One tab per agent harness, following the WAI-ARIA tabs pattern with
 * automatic activation like PresetTabs.vue. Every panel is rendered on the
 * server; AgentHooks.astro un-hides them all when JavaScript is off.
 */
import { nextTick, ref } from 'vue';
import CopyButton from './CopyButton.vue';
import { agentHooks } from '../lib/agent-hooks';
import { nextIndex } from '../lib/tabs';

const selected = ref(0);
const tabEls = ref<HTMLElement[]>([]);

function onKeydown(event: KeyboardEvent, current: number) {
  const target = nextIndex(current, event.key, agentHooks.length);
  if (target === null) return;
  event.preventDefault();
  selected.value = target;
  // Roving tabindex: focus follows the selection once the DOM has updated.
  void nextTick(() => tabEls.value[target]?.focus());
}
</script>

<template>
  <div class="card">
    <div class="tablist" role="tablist" aria-label="Agent harnesses">
      <button
        v-for="(hook, i) in agentHooks"
        :id="`hook-tab-${hook.id}`"
        :key="hook.id"
        :ref="(el) => { if (el) tabEls[i] = el as HTMLElement; }"
        type="button"
        role="tab"
        class="tab"
        :class="{ active: i === selected }"
        :aria-selected="i === selected"
        :aria-controls="`hook-panel-${hook.id}`"
        :tabindex="i === selected ? 0 : -1"
        @click="selected = i"
        @keydown="onKeydown($event, i)"
      >
        {{ hook.label }}
      </button>
    </div>

    <div
      v-for="(hook, i) in agentHooks"
      :id="`hook-panel-${hook.id}`"
      :key="hook.id"
      role="tabpanel"
      class="panel"
      :aria-labelledby="`hook-tab-${hook.id}`"
      tabindex="0"
      :hidden="i !== selected"
    >
      <div class="snippet">
        <div class="snippet-head">
          <code class="file">{{ hook.file }}</code>
          <CopyButton :text="hook.snippet" :label="`Copy ${hook.label} hook`" />
        </div>
        <pre tabindex="0"><code>{{ hook.snippet }}</code></pre>
      </div>
    </div>
  </div>
</template>

<style scoped>
.tablist {
  display: flex;
  gap: var(--space-1);
  margin-bottom: var(--space-4);
  border-bottom: 1px solid var(--border);
  /* Scrolls inside the card on narrow screens instead of widening the page. */
  overflow-x: auto;
}

.tab {
  flex: none;
  min-height: 44px;
  padding: 0 var(--space-3);
  border: 0;
  border-bottom: 3px solid transparent;
  background: transparent;
  color: var(--text-muted);
  font: inherit;
  font-size: 0.95rem;
  white-space: nowrap;
  cursor: pointer;
}

.tab:hover {
  color: var(--text);
}

.tab.active {
  color: var(--text);
  border-bottom-color: var(--violet);
  font-weight: 700;
}

.panel[hidden] {
  display: none;
}

.snippet {
  border: 1px solid var(--border);
  border-radius: var(--radius);
  background: var(--bg-elevated);
  padding: var(--space-2) var(--space-4) var(--space-3);
  max-width: 100%;
}

.snippet-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--space-2);
  margin-bottom: var(--space-2);
}

.file {
  color: var(--neon);
  font-size: 0.85rem;
}

pre {
  overflow-x: auto;
}
</style>
