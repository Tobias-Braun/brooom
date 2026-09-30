/**
 * Index of the tab a key press moves to, following the WAI-ARIA tabs
 * pattern with automatic activation: arrows wrap at both ends, Home and End
 * jump to the first and last tab. Returns `null` for any other key (or an
 * empty list) so the caller can leave the event alone.
 */
export function nextIndex(current: number, key: string, length: number): number | null {
  if (length <= 0) return null;
  switch (key) {
    case 'ArrowRight':
      return (current + 1) % length;
    case 'ArrowLeft':
      return (current - 1 + length) % length;
    case 'Home':
      return 0;
    case 'End':
      return length - 1;
    default:
      return null;
  }
}
