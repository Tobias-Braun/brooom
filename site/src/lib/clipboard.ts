/**
 * Copy outcome: `copied` means the text is on the clipboard, `fallback` means
 * the automatic copy failed but the text was selected so the visitor can
 * press Ctrl/Cmd+C, `failed` means nothing could be done.
 */
export type CopyResult = 'copied' | 'fallback' | 'failed';

/** How long the hidden manual-copy selection is kept before it is dropped. */
const FALLBACK_SELECTION_MS = 15000;

/**
 * Copies text. Safari only allows clipboard writes inside a user gesture, so
 * call this straight from the click handler: when the async Clipboard API is
 * unavailable the legacy path runs synchronously, before the first await.
 * When the API exists but rejects (permissions, insecure frame) the legacy
 * path runs afterwards, which still works in browsers that keep the gesture
 * alive across a microtask.
 */
export async function copyText(text: string, doc: Document = document): Promise<CopyResult> {
  const view = doc.defaultView;
  if (view?.navigator.clipboard && view.isSecureContext) {
    try {
      await view.navigator.clipboard.writeText(text);
      return 'copied';
    } catch {
      // Fall through to the legacy path below.
    }
  }
  return legacyCopy(text, doc);
}

/**
 * Selects the text in a temporary textarea and runs `execCommand('copy')`.
 * The previously focused element and the visitor's selection are restored
 * afterwards. If the command fails or throws, the text is selected in a
 * hidden element instead so a manual copy works, and `fallback` is returned.
 */
function legacyCopy(text: string, doc: Document): CopyResult {
  const body = doc.body;
  if (!body) return 'failed';
  // The typeof guard keeps the function usable in non-DOM test environments.
  const active =
    typeof HTMLElement !== 'undefined' && doc.activeElement instanceof HTMLElement ? doc.activeElement : null;
  const selection = doc.getSelection();
  const saved = selection && selection.rangeCount > 0 ? selection.getRangeAt(0) : null;

  const area = doc.createElement('textarea');
  area.value = text;
  area.setAttribute('readonly', '');
  area.setAttribute('aria-hidden', 'true');
  // Off-screen but selectable; 16px stops iOS from zooming on focus.
  area.style.cssText = 'position:fixed;top:0;left:-9999px;opacity:0;font-size:16px;';
  body.appendChild(area);
  area.focus({ preventScroll: true });
  area.select();
  // iOS ignores select() on textareas without an explicit range.
  area.setSelectionRange(0, text.length);

  let done = false;
  try {
    done = doc.execCommand('copy');
  } catch {
    done = false;
  }

  body.removeChild(area);
  active?.focus({ preventScroll: true });
  if (done) {
    restoreSelection(selection, saved);
    return 'copied';
  }
  return selectForManualCopy(text, doc, selection) ? 'fallback' : 'failed';
}

function restoreSelection(selection: Selection | null, saved: Range | null): void {
  if (!selection) return;
  selection.removeAllRanges();
  if (saved) selection.addRange(saved);
}

/**
 * Selects `text` inside a hidden element so Ctrl/Cmd+C copies it. The element
 * is removed after the next copy or after a timeout, whichever comes first.
 */
function selectForManualCopy(text: string, doc: Document, selection: Selection | null): boolean {
  if (!selection || !doc.body) return false;
  const holder = doc.createElement('pre');
  holder.textContent = text;
  holder.style.cssText = 'position:fixed;top:0;left:-9999px;margin:0;';
  doc.body.appendChild(holder);
  const range = doc.createRange();
  range.selectNodeContents(holder);
  selection.removeAllRanges();
  selection.addRange(range);

  const view = doc.defaultView;
  const cleanup = () => {
    holder.remove();
    doc.removeEventListener('copy', cleanup);
  };
  doc.addEventListener('copy', cleanup);
  view?.setTimeout(cleanup, FALLBACK_SELECTION_MS);
  return true;
}
