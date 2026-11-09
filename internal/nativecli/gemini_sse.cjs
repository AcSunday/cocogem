'use strict';

// This preload applies only to the Gemini model stream. It does not change the
// configured endpoint, credentials, request body, or native tool permissions.
const originalOptions = process.env.COCOGEM_GEMINI_ORIGINAL_NODE_OPTIONS;
if (process.env.GEMINI_CLI_NO_RELAUNCH || process.env.SANDBOX) {
  if (process.env.COCOGEM_GEMINI_NODE_OPTIONS_PRESENT === '1') {
    process.env.NODE_OPTIONS = originalOptions;
  } else {
    delete process.env.NODE_OPTIONS;
  }
  delete process.env.COCOGEM_GEMINI_ORIGINAL_NODE_OPTIONS;
  delete process.env.COCOGEM_GEMINI_NODE_OPTIONS_PRESENT;
}

const originalFetch = globalThis.fetch;
if (typeof originalFetch === 'function') {
  globalThis.fetch = async function(input, init) {
    const response = await originalFetch.call(this, input, init);
    let pathname;
    try {
      pathname = new URL(typeof input === 'string' || input instanceof URL ? input : input.url).pathname;
    } catch (_) {
      return response;
    }
    if (!/(?:%3a|:)streamGenerateContent$/i.test(pathname) ||
        !response.ok || !response.body ||
        !/^text\/event-stream(?:\s*;|\s*$)/i.test(response.headers.get('content-type') || '')) {
      return response;
    }
    const body = response.body.pipeThrough(geminiStreamFilter());
    return new Proxy(response, {
      get(target, property) {
        if (property === 'body') return body;
        const value = Reflect.get(target, property, target);
        return typeof value === 'function' ? value.bind(target) : value;
      }
    });
  };
}

// Bound line buffering: long data lines pass through incrementally and are
// left to the CLI parser. Only short, complete error events are classified.
function geminiStreamFilter() {
  const limit = 64 * 1024;
  let parts = [];
  let length = 0;
  let comment = false;
  let started = false;
  let passthrough = false;
  let skipLF = false;

  function append(piece, controller) {
    if (!piece.length) return;
    if (!started) {
      started = true;
      comment = piece[0] === 58;
    }
    if (comment) return;
    if (passthrough) {
      controller.enqueue(piece);
    } else if (length + piece.length > limit) {
      for (const part of parts) controller.enqueue(part);
      parts = [];
      controller.enqueue(piece);
      passthrough = true;
    } else {
      parts.push(piece.slice());
      length += piece.length;
    }
  }

  function finish(delimiter, controller) {
    if (comment) {
      skipLF = delimiter === 13;
    } else {
      if (!passthrough && length) {
        const line = Buffer.concat(parts, length);
        const failure = geminiStreamError(line);
        if (failure) throw new Error('COCOGEM_GEMINI_SSE_ERROR_' + failure);
        controller.enqueue(line);
      }
      controller.enqueue(Uint8Array.of(delimiter));
    }
    parts = [];
    length = 0;
    comment = false;
    started = false;
    passthrough = false;
  }

  return new TransformStream({
    transform(chunk, controller) {
      let start = 0;
      if (skipLF && chunk.length) {
        if (chunk[0] === 10) start = 1;
        skipLF = false;
      }
      for (let i = start; i < chunk.length; i++) {
        if (chunk[i] !== 10 && chunk[i] !== 13) continue;
        append(chunk.subarray(start, i), controller);
        finish(chunk[i], controller);
        start = i + 1;
        if (skipLF && start < chunk.length) {
          if (chunk[start] === 10) start++;
          skipLF = false;
          i = start - 1;
        }
      }
      append(chunk.subarray(start), controller);
    },
    flush(controller) {
      if (!comment && !passthrough) {
        for (const part of parts) controller.enqueue(part);
      }
    }
  });
}

function geminiStreamError(line) {
  if (!line.subarray(0, 5).equals(Buffer.from('data:'))) return '';
  let event;
  try {
    event = JSON.parse(line.subarray(5).toString('utf8').trim());
  } catch (_) {
    return '';
  }
  if (!event || typeof event !== 'object' || Array.isArray(event) ||
      (!event.error && event.type !== 'error')) return '';
  const error = event.error && typeof event.error === 'object' ? event.error : {};
  const code = String(error.code ?? error.status ?? event.code ?? '').toLowerCase();
  if (['500', '502', '503', '504', 'server_error', 'internal_error', 'internal', 'unavailable', 'overloaded'].includes(code)) return 'TRANSIENT';
  if (['429', 'resource_exhausted', 'rate_limit_exceeded', 'quota_exceeded'].includes(code)) return 'QUOTA';
  if (['401', 'unauthenticated', 'unauthorized', 'authentication_error', 'invalid_api_key'].includes(code)) return 'AUTHENTICATION';
  if (['403', 'permission_denied'].includes(code)) return 'PERMISSION';
  return 'UNKNOWN';
}
