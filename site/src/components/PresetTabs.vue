<script setup lang="ts">
/**
 * One tab per sweep preset, following the WAI-ARIA tabs pattern with
 * automatic activation like InstallTabs.vue. Every panel is rendered on the
 * server; PresetsSlot.astro un-hides them all when JavaScript is off.
 */
import { nextTick, ref } from 'vue';
import { presets, sweepFlags } from '../lib/presets';
import { nextIndex } from '../lib/tabs';

const selected = ref(0);
const tabEls = ref<HTMLElement[]>([]);

function onKeydown(event: KeyboardEvent, current: number) {
  const target = nextIndex(current, event.key, presets.length);
  if (target === null) return;
  event.preventDefault();
  selected.value = target;
  // Roving tabindex: focus follows the selection once the DOM has updated.
  void nextTick(() => tabEls.value[target]?.focus());
}
</script>

<template>
  <div class="card presets">
    <div class="tablist" role="tablist" aria-label="Sweep presets">
      <button
        v-for="(preset, i) in presets"
        :id="`preset-tab-${preset.id}`"
        :key="preset.id"
        :ref="(el) => { if (el) tabEls[i] = el as HTMLElement; }"
        type="button"
        role="tab"
        class="tab"
        :class="{ active: i === selected }"
        :aria-selected="i === selected"
        :aria-controls="`preset-panel-${preset.id}`"
        :tabindex="i === selected ? 0 : -1"
        @click="selected = i"
        @keydown="onKeydown($event, i)"
      >
        {{ preset.id }}
        <span v-if="preset.isDefault" class="badge">default</span>
      </button>
    </div>

    <div
      v-for="(preset, i) in presets"
      :id="`preset-panel-${preset.id}`"
      :key="preset.id"
      role="tabpanel"
      class="panel"
      :aria-labelledby="`preset-tab-${preset.id}`"
      tabindex="0"
      :hidden="i !== selected"
    >
      <pre class="command" tabindex="0"><code>br sweep {{ preset.id }}<template v-for="f in sweepFlags" :key="f.flag"> <span class="optional">[{{ f.flag }}]</span></template></code></pre>
      <p class="flags">
        <template v-for="(f, n) in sweepFlags" :key="f.flag">
          <code>{{ f.flag }}</code> {{ f.meaning }}{{ n < sweepFlags.length - 1 ? '; ' : '.' }}
        </template>
        <template v-if="preset.isDefault"> Plain <code>br sweep</code> runs this preset unless <code>sweep.preset</code> in the config names another.</template>
      </p>

      <h3>What it sweeps</h3>
      <ul class="rules">
        <li v-for="line in preset.includes" :key="line">{{ line }}</li>
      </ul>

      <dl class="facts">
        <dt>Detectors</dt>
        <dd>
          <ul class="chips">
            <li v-for="d in preset.detectors" :key="d">
              <a class="chip" :href="`#detector-${d}`">{{ d }}</a>
            </li>
          </ul>
        </dd>
        <dt>Minimum confidence</dt>
        <dd>
          {{ preset.minConfidence }}<template v-for="(c, d) in preset.floors" :key="d"> ({{ d }}: {{ c }})</template>
        </dd>
      </dl>
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
  font-family: var(--font-mono);
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
  color: var(--neon);
  font-family: var(--font-sans);
  font-size: 0.72rem;
  font-weight: 400;
  letter-spacing: 0.03em;
}

.panel[hidden] {
  display: none;
}

.command {
  padding: var(--space-3) var(--space-4);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  background: var(--bg-elevated);
  font-size: 1rem;
  /* The optional flags wrap on phones instead of hiding behind a scroll. */
  white-space: pre-wrap;
}

.optional {
  color: var(--text-muted);
}

.flags {
  margin: var(--space-2) 0 var(--space-4);
  color: var(--text-muted);
  font-size: 0.9rem;
}

.rules {
  margin: 0 0 var(--space-4);
  padding-left: var(--space-4);
}

.rules li {
  margin-bottom: var(--space-1);
}

.facts {
  display: grid;
  grid-template-columns: max-content 1fr;
  gap: var(--space-2) var(--space-4);
  align-items: baseline;
  margin: 0;
}

.facts dt {
  color: var(--text-muted);
  font-size: 0.9rem;
}

.facts dd {
  margin: 0;
}

.chips {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-2);
  margin: 0;
  padding: 0;
  list-style: none;
}

.chips a {
  font-family: var(--font-mono);
  text-decoration: none;
}

.chips a:hover {
  border-color: var(--violet);
}

@media (max-width: 30rem) {
  .facts {
    grid-template-columns: 1fr;
  }
}
</style>
