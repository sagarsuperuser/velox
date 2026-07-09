import js from '@eslint/js'
import globals from 'globals'
import reactHooks from 'eslint-plugin-react-hooks'
import reactRefresh from 'eslint-plugin-react-refresh'
import tseslint from 'typescript-eslint'
import { defineConfig, globalIgnores } from 'eslint/config'

// Relative-time gate (ADR-086 Phase 4): raw `Date.now()` / argless `new Date()`
// is wall-clock now — on a clock-pinned (test-clock-simulated) entity that's a
// lie. All age/countdown/window UI must anchor on an EffectiveNow built via
// effectiveNow(frozenISO) or wallClockNow() (src/lib/effectiveNow.ts), which the
// type system already makes non-optional at the helper boundary. This rule is
// the second, independent gate: it stops someone hand-rolling new relative-time
// math with raw wall-clock instead of using those helpers. Genuine calendar /
// date-picker / infra uses opt out with a one-line eslint-disable + reason, or
// live in the date-infra modules exempted below.
const noWallClockNow = [
  'error',
  {
    selector: "CallExpression[callee.object.name='Date'][callee.property.name='now']",
    message:
      'Raw Date.now() ignores test clocks. Use effectiveNow(frozenISO) or wallClockNow() from @/lib/effectiveNow for relative-time. For genuine wall-clock/calendar/infra use, add `// eslint-disable-next-line no-restricted-syntax -- <reason>`.',
  },
  {
    selector: "NewExpression[callee.name='Date'][arguments.length=0]",
    message:
      'Argless `new Date()` is wall-clock now. Use effectiveNow()/wallClockNow() from @/lib/effectiveNow for relative-time. For calendar/date-picker/infra use, add `// eslint-disable-next-line no-restricted-syntax -- <reason>` (or keep it in lib/dates.ts).',
  },
]

export default defineConfig([
  globalIgnores(['dist']),
  {
    files: ['**/*.{ts,tsx}'],
    extends: [
      js.configs.recommended,
      tseslint.configs.recommended,
      reactHooks.configs.flat.recommended,
      reactRefresh.configs.vite,
    ],
    languageOptions: {
      ecmaVersion: 2020,
      globals: globals.browser,
    },
    rules: {
      'no-restricted-syntax': noWallClockNow,
    },
  },
  {
    // Wall-clock IS the domain here: the date/timezone utility module and the
    // calendar picker component. Tests seed fixtures freely.
    files: ['src/lib/dates.ts', 'src/components/ui/date-picker.tsx', '**/*.test.{ts,tsx}'],
    rules: {
      'no-restricted-syntax': 'off',
    },
  },
])
