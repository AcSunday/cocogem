// Development startup aligns public web access with the OS or environment proxy
// via "auto" unless explicitly configured. An explicit COCOGEM_WEB_PROXY
// (including empty or "off") wins.
export function backendEnv(environ) {
  if ('COCOGEM_WEB_PROXY' in environ) return environ;
  return { ...environ, COCOGEM_WEB_PROXY: 'auto' };
}
