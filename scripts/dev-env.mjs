// Development startup aligns public web access with the OS proxy the developer
// already configured. An explicit COCOGEM_WEB_PROXY (including empty) wins.
export function backendEnv(environ) {
  if ('COCOGEM_WEB_PROXY' in environ) return environ;
  return { ...environ, COCOGEM_WEB_PROXY: 'system' };
}
