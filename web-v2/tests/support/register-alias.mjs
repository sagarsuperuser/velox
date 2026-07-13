// `node --test --import ./tests/support/register-alias.mjs` installs the `@/…`
// resolver (see alias-hooks.mjs) so tests can import src/ modules that use the
// vite path alias.
import { register } from 'node:module'
register('./alias-hooks.mjs', import.meta.url)
