<script setup lang="ts">
/**
 * Install tabs following the WAI-ARIA tabs pattern with automatic
 * activation. The first tab is selected on the server, so the initial HTML
 * is a meaningful panel before hydration; a remembered selection is applied
 * after mounting only. Coming-soon tabs stay focusable and selectable, they
 * explain their status instead of being disabled.
 */
import { nextTick, onMounted, ref } from 'vue';
import CopyButton from './CopyButton.vue';
import { installTabs, TAB_STORAGE_KEY } from '../lib/install-tabs';
import { nextIndex } from '../lib/tabs';

const selected = ref(0);
const tabEls = ref<HTMLElement[]>([]);

onMounted(() => {
  try {
    const saved = window.localStorage.getItem(TAB_STORAGE_KEY);
    const index = installTabs.findIndex((t) => t.id === saved);
    if (index >= 0) selected.value = index;
  } catch {
    // Storage can be blocked or absent; the page works without it.
  }
});

function select(index: number) {
  selected.value = index;
  try {
    window.localStorage.setItem(TAB_STORAGE_KEY, installTabs[index]!.id);
  } catch {
    // Remembering the tab is a convenience only.
  }
}

function onKeydown(event: KeyboardEvent, current: number) {
  const target = nextIndex(current, event.key, installTabs.length);
  if (target === null) return;
  event.preventDefault();
  select(target);
  // Roving tabindex: focus follows the selection once the DOM has updated.
  void nextTick(() => tabEls.value[target]?.focus());
}
</script>

<template>
  <div id="install" data-slot="install" class="card">
    <div class="tablist" role="tablist" aria-label="Installation methods">
      <button
        v-for="(tab, i) in installTabs"
        :id="`install-tab-${tab.id}`"
        :key="tab.id"
        :ref="(el) => { if (el) tabEls[i] = el as HTMLElement; }"
        type="button"
        role="tab"
        class="tab"
        :class="{ active: i === selected }"
        :aria-selected="i === selected"
        :aria-controls="`install-panel-${tab.id}`"
        :tabindex="i === selected ? 0 : -1"
        @click="select(i)"
        @keydown="onKeydown($event, i)"
      >
        {{ tab.label }}
        <span v-if="tab.status === 'soon'" class="badge">coming soon</span>
      </button>
    </div>

    <div
      v-for="(tab, i) in installTabs"
      :id="`install-panel-${tab.id}`"
      :key="tab.id"
      role="tabpanel"
      class="panel"
      :aria-labelledby="`install-tab-${tab.id}`"
      tabindex="0"
      :hidden="i !== selected"
    >
      <template v-for="(block, b) in tab.blocks" :key="b">
        <p v-if="block.type === 'text'" class="text">{{ block.text }}</p>
        <div v-else-if="block.type === 'command'" class="command" data-copy-scope>
          <div class="command-head">
            <span class="os">{{ block.os ?? 'Any OS' }}</span>
            <CopyButton :text="block.command" :label="block.copyLabel" />
          </div>
          <pre tabindex="0"><code data-copy-source>{{ block.command }}</code></pre>
        </div>
        <p v-else-if="block.type === 'link'" class="links">
          <a :href="block.href">{{ block.label }}</a>
        </p>
        <ul v-else class="archives">
          <li v-for="item in block.items" :key="item"><code>{{ item }}</code></li>
        </ul>
      </template>
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
  display: inline-flex;
  align-items: center;
  gap: var(--space-2);
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

.badge {
  padding: 0.05rem 0.5rem;
  border: 1px solid var(--border);
  border-radius: 999px;
  color: var(--text-muted);
  font-size: 0.72rem;
  font-weight: 400;
  letter-spacing: 0.03em;
}

.panel[hidden] {
  display: none;
}

.text {
  color: var(--text-muted);
  max-width: 44rem;
}

.command {
  margin-bottom: var(--space-3);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  background: var(--bg-elevated);
  padding: var(--space-2) var(--space-4) var(--space-3);
  max-width: 100%;
}

.command-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--space-2);
  margin-bottom: var(--space-2);
}

.os {
  color: var(--neon);
  font-family: var(--font-mono);
  font-size: 0.85rem;
}

.links a {
  display: inline-flex;
  align-items: center;
  min-height: 44px;
}

.archives {
  margin: 0;
  padding-left: var(--space-4);
  overflow-x: auto;
}

.archives li {
  white-space: nowrap;
  margin-bottom: var(--space-2);
}
</style>
