import { clsx } from 'clsx';
import { twMerge } from 'tailwind-merge';

export function cn(...inputs) {
  return twMerge(clsx(inputs));
}

export function formatNumber(num) {
  if (num === null || num === undefined) return '0';
  if (typeof num === 'string') num = parseFloat(num);
  if (isNaN(num)) return '0';
  return new Intl.NumberFormat('zh-CN').format(num);
}

export function formatRate(rate) {
  if (rate === null || rate === undefined || isNaN(rate)) return '—';
  return (rate * 100).toFixed(1) + '%';
}

export function formatDate(timestamp) {
  if (!timestamp) return '—';
  const date = new Date(typeof timestamp === 'number' && timestamp < 10000000000 ? timestamp * 1000 : timestamp);
  return date.toLocaleString('zh-CN', {
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
  });
}

// Legacy clipboard path using a temporary off-screen textarea.
//
// navigator.clipboard only exists in a secure context (https / localhost), so a
// deployment served over plain http://<ip>:<port> has no Clipboard API at all.
// document.execCommand('copy') still works for user-initiated actions there, and
// it is also the fallback when the async API exists but is rejected (e.g. the
// document is not focused or permission was denied).
function legacyCopy(value) {
  if (typeof document === 'undefined' || !document.body) {
    return false;
  }
  // Preserve focus and scroll position: iOS Safari otherwise jumps to the
  // temporarily appended element.
  const activeElement = document.activeElement;
  const scrollY = window.scrollY;

  const textarea = document.createElement('textarea');
  textarea.value = value;
  textarea.setAttribute('readonly', '');
  textarea.setAttribute('aria-hidden', 'true');
  textarea.style.position = 'fixed';
  textarea.style.top = '0';
  textarea.style.left = '-9999px';
  textarea.style.opacity = '0';
  document.body.appendChild(textarea);

  let ok = false;
  try {
    textarea.focus();
    textarea.select();
    textarea.setSelectionRange(0, textarea.value.length);
    ok = document.execCommand('copy');
  } catch (err) {
    ok = false;
  } finally {
    document.body.removeChild(textarea);
    if (activeElement && typeof activeElement.focus === 'function') {
      activeElement.focus();
    }
    window.scrollTo(0, scrollY);
  }
  return ok;
}

/**
 * Copies text to the clipboard, working in both secure and insecure contexts.
 * Returns true only when the text really reached the clipboard, so callers can
 * surface an accurate success or failure message.
 */
export async function copyToClipboard(text) {
  const value = text === null || text === undefined ? '' : String(text);
  if (!value) {
    return false;
  }

  // Preferred path: async Clipboard API, only present in a secure context.
  if (typeof navigator !== 'undefined' && navigator.clipboard?.writeText) {
    try {
      await navigator.clipboard.writeText(value);
      return true;
    } catch (err) {
      // Fall through to the legacy path rather than reporting a false success.
    }
  }

  return legacyCopy(value);
}
