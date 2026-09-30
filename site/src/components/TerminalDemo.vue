<script setup lang="ts">
/**
 * Animated replay of `br sweep --dry-run` and `br sweep`. The initial
 * state is the completed final frame, so server rendering, no-JS viewers and
 * pre-hydration viewers all see the finished output and hydration cannot
 * mismatch. Only after mounting, and only if the visitor allows motion, is
 * the screen cleared and the script replayed. The replay runs only while the
 * component is on screen, the tab is visible and the visitor has not paused.
 */
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue';
import { createRunner, type Runner } from '../lib/runner';
import {
  applyAction,
  expandSteps,
  finalFrame,
  maxScreenLines,
  terminalScript,
  transcript,
  type Line,
} from '../lib/terminal-script';

const ticks = expandSteps(terminalScript);
const textAlternative = transcript(terminalScript);
const maxLines = maxScreenLines(terminalScript);

const screen = ref<Line[]>(finalFrame(terminalScript));

// Whether the animation machinery is active at all (mounted, motion allowed).
const animated = ref(false);
const reduced = ref(false);
const inView = ref(false);
const tabVisible = ref(true);
const userPaused = ref(false);

const shouldRun = computed(() => animated.value && inView.value && tabVisible.value && !userPaused.value);

let runner: Runner | null = null;
let observer: IntersectionObserver | null = null;
let motionQuery: MediaQueryList | null = null;

function begin() {
  screen.value = [];
  runner = createRunner({
    ticks,
    timers: {
      setTimeout: (fn, ms) => window.setTimeout(fn, ms),
      clearTimeout: (id) => window.clearTimeout(id as number),
    },
    onTick: (tick) => {
      screen.value = applyAction(screen.value, tick.action);
    },
    onLoop: () => {
      screen.value = [];
    },
  });
  runner.start();
}

function teardown() {
  runner?.stop();
  runner = null;
}

// Single place that reconciles the four conditions with the runner.
function sync() {
  if (!animated.value) {
    teardown();
    screen.value = finalFrame(terminalScript);
    return;
  }
  if (!shouldRun.value) {
    runner?.pause();
    return;
  }
  if (runner) runner.resume();
  else begin();
}

function onMotionChange() {
  if (!motionQuery) return;
  reduced.value = motionQuery.matches;
  animated.value = !motionQuery.matches;
}

function onVisibility() {
  tabVisible.value = document.visibilityState !== 'hidden';
}

const root = ref<HTMLElement | null>(null);

onMounted(() => {
  motionQuery = window.matchMedia('(prefers-reduced-motion: reduce)');
  motionQuery.addEventListener('change', onMotionChange);
  document.addEventListener('visibilitychange', onVisibility);
  onVisibility();
  if (root.value && 'IntersectionObserver' in window) {
    observer = new IntersectionObserver(
      (entries) => {
        const last = entries[entries.length - 1];
        if (last) inView.value = last.isIntersecting;
      },
      { threshold: 0.25 },
    );
    observer.observe(root.value);
  } else {
    // Without IntersectionObserver the island was hydrated because it was
    // visible (client:visible), so treating it as in view is the best guess.
    inView.value = true;
  }
  onMotionChange();
});

watch([animated, shouldRun], sync);

onBeforeUnmount(() => {
  teardown();
  observer?.disconnect();
  motionQuery?.removeEventListener('change', onMotionChange);
  document.removeEventListener('visibilitychange', onVisibility);
});

function togglePause() {
  userPaused.value = !userPaused.value;
}
</script>

<template>
  <div id="terminal-demo" ref="root" data-slot="terminal-demo" class="terminal">
    <div class="chrome">
      <span class="dots" aria-hidden="true"><span></span><span></span><span></span></span>
      <button
        v-if="animated"
        type="button"
        class="toggle"
        :aria-pressed="userPaused"
        @click="togglePause"
      >
        <svg viewBox="0 0 16 16" width="14" height="14" aria-hidden="true" focusable="false">
          <path v-if="!userPaused" d="M4 2h3v12H4zM9 2h3v12H9z" fill="currentColor" />
          <path v-else d="M4 2l10 6-10 6z" fill="currentColor" />
        </svg>
        Pause animation
      </button>
    </div>
    <div class="scroll" role="group" aria-label="Terminal output, scrollable" tabindex="0" :style="{ '--lines': maxLines }">
      <div class="screen" aria-hidden="true">
        <div v-for="(line, i) in screen" :key="i" class="line" :class="line.tone">
          {{ line.text
          }}<span v-if="animated && i === screen.length - 1" class="cursor"></span>
        </div>
        <div v-if="animated && screen.length === 0" class="line"><span class="cursor"></span></div>
      </div>
      <!-- Inside the focusable group so screen reader users who land on it hear the text, not an empty group. -->
      <pre class="visually-hidden">{{ textAlternative }}</pre>
    </div>
  </div>
</template>

<style scoped>
.terminal {
  max-width: 100%;
  border: 1px solid var(--border);
  border-radius: var(--radius);
  background: var(--bg-elevated);
  box-shadow: 0 0 40px rgb(96 165 250 / 0.12);
  overflow: hidden;
  position: relative;
}

.chrome {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--space-2);
  min-height: 44px;
  padding: 0 var(--space-3);
  border-bottom: 1px solid var(--border);
  background: var(--surface);
}

.dots {
  display: flex;
  gap: 6px;
}

.dots span {
  width: 10px;
  height: 10px;
  border-radius: 50%;
  background: var(--border);
}

.toggle {
  display: inline-flex;
  align-items: center;
  gap: var(--space-2);
  min-width: 44px;
  min-height: 44px;
  padding: 0 var(--space-2);
  border: 0;
  background: transparent;
  color: var(--text-muted);
  font: inherit;
  font-size: 0.85rem;
  cursor: pointer;
}

.toggle:hover {
  color: var(--text);
}

/*
 * The height is reserved for the tallest frame (--lines is computed from the
 * script) plus padding and a possible horizontal scrollbar, so nothing below
 * the terminal moves while it types. Overflow scrolls inside the box.
 */
.scroll {
  height: calc(var(--lines) * 1.5em + 3rem);
  padding: var(--space-3) var(--space-4);
  overflow: auto;
  font-family: var(--font-mono);
  font-size: 0.85rem;
  line-height: 1.5;
}

.screen {
  width: max-content;
  min-width: 100%;
}

.line {
  min-height: 1.5em;
  white-space: pre;
  color: var(--text);
}

.line.cmd,
.line.ok {
  color: var(--neon);
}

.line.bold {
  font-weight: 700;
}

.line.dim {
  color: var(--text-muted);
}

.cursor {
  display: inline-block;
  width: 0.6em;
  height: 1.1em;
  margin-left: 1px;
  vertical-align: text-bottom;
  background: var(--neon);
}

@media (prefers-reduced-motion: no-preference) {
  .cursor {
    animation: blink 1.1s steps(1) infinite;
  }
}

@keyframes blink {
  50% {
    opacity: 0;
  }
}
</style>
