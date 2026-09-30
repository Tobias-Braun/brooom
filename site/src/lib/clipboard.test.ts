import { describe, expect, it, vi } from 'vitest';
import { copyText } from './clipboard';

/**
 * A minimal Document double. Only what copyText touches is implemented; the
 * legacy path is exercised through the execCommand and selection stubs.
 */
function fakeDoc(opts: {
  clipboard?: { writeText: (t: string) => Promise<void> };
  secure?: boolean;
  execCommand?: (cmd: string) => boolean;
  withSelection?: boolean;
}) {
  const appended: unknown[] = [];
  const selection = opts.withSelection
    ? {
        rangeCount: 0,
        removeAllRanges: vi.fn(),
        addRange: vi.fn(),
        getRangeAt: vi.fn(),
      }
    : null;
  const textarea = {
    value: '',
    style: { cssText: '' },
    setAttribute: vi.fn(),
    focus: vi.fn(),
    select: vi.fn(),
    setSelectionRange: vi.fn(),
  };
  const holder = { textContent: '', style: { cssText: '' }, remove: vi.fn() };
  const doc = {
    defaultView: {
      navigator: { clipboard: opts.clipboard },
      isSecureContext: opts.secure ?? true,
      setTimeout: vi.fn(),
    },
    body: {
      appendChild: (n: unknown) => appended.push(n),
      removeChild: vi.fn(),
    },
    activeElement: null,
    createElement: (tag: string) => (tag === 'textarea' ? textarea : holder),
    createRange: () => ({ selectNodeContents: vi.fn() }),
    getSelection: () => selection,
    execCommand: opts.execCommand ?? (() => false),
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
  };
  return { doc: doc as unknown as Document, textarea, selection, appended };
}

describe('copyText', () => {
  it('uses the async clipboard API in a secure context', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    const { doc } = fakeDoc({ clipboard: { writeText } });
    await expect(copyText('go install x', doc)).resolves.toBe('copied');
    expect(writeText).toHaveBeenCalledWith('go install x');
  });

  it('falls back to execCommand when the API is missing', async () => {
    const exec = vi.fn().mockReturnValue(true);
    const { doc, textarea } = fakeDoc({ execCommand: exec });
    await expect(copyText('abc', doc)).resolves.toBe('copied');
    expect(exec).toHaveBeenCalledWith('copy');
    expect(textarea.value).toBe('abc');
    expect(textarea.setSelectionRange).toHaveBeenCalledWith(0, 3);
  });

  it('falls back when the context is insecure even if the API exists', async () => {
    const writeText = vi.fn();
    const { doc } = fakeDoc({ clipboard: { writeText }, secure: false, execCommand: () => true });
    await expect(copyText('abc', doc)).resolves.toBe('copied');
    expect(writeText).not.toHaveBeenCalled();
  });

  it('falls back when the API rejects', async () => {
    const writeText = vi.fn().mockRejectedValue(new Error('denied'));
    const { doc } = fakeDoc({ clipboard: { writeText }, execCommand: () => true });
    await expect(copyText('abc', doc)).resolves.toBe('copied');
    expect(writeText).toHaveBeenCalled();
  });

  it('asks for a manual copy when execCommand fails but selection works', async () => {
    const { doc, selection } = fakeDoc({ execCommand: () => false, withSelection: true });
    await expect(copyText('abc', doc)).resolves.toBe('fallback');
    expect(selection?.addRange).toHaveBeenCalled();
  });

  it('selects the visible code in place when it shows the same text', async () => {
    const { doc, appended } = fakeDoc({ execCommand: () => false, withSelection: true });
    const visible = { textContent: '  abc\n' } as unknown as Node;
    await expect(copyText('abc', doc, visible)).resolves.toBe('fallback');
    // Only the temporary textarea was appended, no off-screen holder.
    expect(appended).toHaveLength(1);
  });

  it('treats a throwing execCommand like a failed one', async () => {
    const { doc } = fakeDoc({
      execCommand: () => {
        throw new Error('nope');
      },
      withSelection: true,
    });
    await expect(copyText('abc', doc)).resolves.toBe('fallback');
  });

  it('reports failure when nothing can select the text', async () => {
    const { doc } = fakeDoc({ execCommand: () => false, withSelection: false });
    await expect(copyText('abc', doc)).resolves.toBe('failed');
  });
});
