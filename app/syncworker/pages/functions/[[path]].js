// Pages provides the public URL; the existing Worker owns KV and authentication.
export function onRequest({ request, env }) {
  return env.SYNC_BACKEND.fetch(request);
}
