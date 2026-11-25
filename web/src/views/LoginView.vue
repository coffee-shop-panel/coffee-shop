<script setup>
import logo from '../assets/logo.png'
import { nextTick, ref, computed, onMounted, onUnmounted } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import { store, t, signIn, signInWithPasskey, pickLoginLocale } from '../lib/store.js'
import { passkeySupported } from '../lib/passkey.js'
import { localeName, localeFlag } from '../lib/locales.js'
import AntIcon from '../components/AntIcon.vue'
import { themeMode, cycleTheme } from '../lib/theme.js'

const router = useRouter()
const route = useRoute()

const username = ref('admin')
const password = ref('')
const error = ref('')
const code = ref('')
const needCode = ref(false)
const busy = ref(false)
const langOpen = ref(false)
const showPw = ref(false)

// The fingerprint door, offered only where it can work: a browser with
// the API, on an encrypted origin. Anywhere else the panel's endpoints
// refuse the ceremony too, so the button simply is not drawn.
const canPasskey = passkeySupported()
const pkBusy = ref(false)
const pkError = ref('')

// The headline cycles a word at a time, the way the classic panel's does. It is the one
// piece of motion on an otherwise still screen, so it carries the page.
// Two words, turn and turn about, as the classic panel's headline does.
const words = computed(() => [t('login.hello'), t('login.title')])
const wordIndex = ref(0)
const word = computed(() => words.value[wordIndex.value % words.value.length])
let cycle = null

onMounted(() => {
  const reduced = window.matchMedia?.('(prefers-reduced-motion: reduce)').matches
  if (!reduced) cycle = setInterval(() => wordIndex.value++, 2600)
})
onUnmounted(() => clearInterval(cycle))

async function submit() {
  error.value = ''
  busy.value = true
  try {
    const res = await signIn(username.value, password.value, code.value)
    if (res && res.needCode) {
      needCode.value = true
      // Focus lands on the code box so the operator can type straight away
      // rather than hunting for a field that has just appeared. nextTick
      // rather than an animation frame: a frame never arrives in a tab the
      // browser has backgrounded, and the field would stay unfocused.
      await nextTick()
      document.getElementById('login-code')?.focus()
      return
    }
    goNext()
  } catch (err) {
    error.value = err.message
  } finally {
    busy.value = false
  }
}

// goNext is where a sign-in of either kind lands: the page the sign-in
// was interrupted on its way to, or home -- the owner's overview for
// the owner, and every other operator's own dashboard for them.
// Only a path on this panel: a link crafted with another site in it
// would otherwise be followed straight after a correct password.
function goNext() {
  const next = typeof route.query.next === 'string' ? route.query.next : ''
  const home = store.admin?.managesPanel ? '/' : '/home'
  router.replace(/^\/(?![/\\])/.test(next) ? next : home)
}

async function passkeySignIn() {
  pkError.value = ''
  pkBusy.value = true
  try {
    await signInWithPasskey()
    goNext()
  } catch (err) {
    // A dismissed fingerprint prompt is the browser's own error, and it
    // means "not now" rather than "no": a quiet word, in the operator's
    // language, where the panel's refusals arrive in the server's.
    pkError.value =
      err?.name === 'NotAllowedError'
        ? t('auth.passkeyCancelled')
        : err.message || t('auth.passkeyFailed')
  } finally {
    pkBusy.value = false
  }
}

async function pickLocale(l) {
  langOpen.value = false
  await pickLoginLocale(l)
}

const locales = computed(() => store.meta?.locales || ['en'])
const localeIcon = localeFlag
const version = computed(() => store.meta?.version || '')
</script>

<template>
  <div class="login-app">
    <!-- The design's art panel: the brand, the headline the page already
         turns a word at a time, and what the panel is — over the design's
         glow-and-grid ground. It goes away on a narrow screen, where the
         form is the whole page. -->
    <aside class="login-art">
      <div class="art-brand">
        <img class="brand-logo" :src="logo" alt="" />
        <span class="art-wordmark">{{ t('app.name') }}</span>
      </div>
      <div class="art-mid">
        <h1 class="art-headline"><b :key="word">{{ word }}</b></h1>
        <p class="art-tagline">{{ t('app.tagline') }}</p>
      </div>
      <div v-if="version" class="art-foot">{{ version }}</div>
    </aside>

    <div class="login-content">
      <div class="login-box">
        <!-- On a narrow screen the art panel is gone, so the brand rides at
             the head of the box rather than leaving the page nameless. -->
        <div class="box-brand">
          <img class="brand-logo" :src="logo" alt="" />
          <span class="art-wordmark">{{ t('app.name') }}</span>
        </div>

        <!-- The design's box head: the way in, and the theme and language
             buttons beside it — the two the page has always carried, moved
             from their fixed corner into the box. -->
        <div class="box-head">
          <div>
            <h2>{{ t('auth.signIn') }}</h2>
            <p class="box-sub">{{ t('login.welcome') }}</p>
          </div>
          <div class="box-tools">
            <button class="tool-btn" type="button" :title="t('nav.theme')" :aria-label="t('nav.theme')" @click="cycleTheme">
              <AntIcon :name="themeMode === 'light' ? 'SunOutlined' : themeMode === 'dark' ? 'MoonOutlined' : 'MoonFilled'" />
            </button>
            <div class="lang">
              <button class="tool-btn" type="button" :aria-label="t('settings.language')" :aria-expanded="langOpen" @click="langOpen = !langOpen">
                <AntIcon name="TranslationOutlined" />
              </button>
              <div v-if="langOpen" class="amenu lang-menu" role="menu">
                <button v-for="l in locales" :key="l" class="amenu-item" :class="{ selected: l === store.locale }" role="menuitem" @click="pickLocale(l)">
                  <span aria-hidden="true">{{ localeIcon(l) }}</span><span>{{ localeName(l) }}</span>
                </button>
              </div>
            </div>
          </div>
        </div>

        <form class="login-form" @submit.prevent="submit">
          <div class="aform-item">
            <label class="aform-label" for="login-user">{{ t('auth.username') }}</label>
            <label class="ainput block large">
              <span class="ainput-prefix"><AntIcon name="UserOutlined" /></span>
              <input id="login-user" v-model="username" autocomplete="username" :placeholder="t('auth.username')" autofocus required />
            </label>
          </div>
          <div class="aform-item">
            <label class="aform-label" for="login-pass">{{ t('auth.password') }}</label>
            <label class="ainput block large">
              <span class="ainput-prefix"><AntIcon name="LockOutlined" /></span>
              <input id="login-pass" v-model="password" :type="showPw ? 'text' : 'password'" autocomplete="current-password" :placeholder="t('auth.password')" required />
              <button type="button" class="ainput-eye" :aria-label="t('auth.password')" @click="showPw = !showPw"><AntIcon :name="showPw ? 'EyeOutlined' : 'EyeInvisibleOutlined'" /></button>
            </label>
          </div>
          <!-- Only after the password was accepted, so this reveals nothing
               to someone guessing at the username. The design sets the step
               in a calm card of its own; the field still rides the one form,
               so the submit above it stays the only way in. -->
          <div v-if="needCode" class="aform-item code-card">
            <label class="aform-label" for="login-code">{{ t('auth.code') }}</label>
            <label class="ainput block large">
              <span class="ainput-prefix"><AntIcon name="KeyOutlined" /></span>
              <input id="login-code" v-model="code" inputmode="numeric" autocomplete="one-time-code" maxlength="6" :placeholder="t('auth.code')" class="ltr" required />
            </label>
          </div>

          <p v-if="error" class="error" role="alert">{{ error }}</p>

          <div class="aform-item submit-row">
            <button class="abtn primary large block" type="submit" :disabled="busy">
              <span v-if="busy" class="spin"></span>
              <template v-else>{{ t('auth.signIn') }}</template>
            </button>
          </div>
        </form>

        <!-- The fingerprint door, under the password door: the same
             session, without the typing. Drawn only where the browser
             and the encrypted origin allow it at all. -->
        <div v-if="canPasskey" class="passkey-block">
          <div class="passkey-divider"><span>{{ t('login.or') }}</span></div>
          <p v-if="pkError" class="error" role="alert">{{ pkError }}</p>
          <button class="abtn large block" type="button" :disabled="pkBusy" @click="passkeySignIn">
            <span v-if="pkBusy" class="spin"></span>
            <template v-else>
              <AntIcon name="SafetyOutlined" /> {{ t('auth.passkey') }}
            </template>
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
/* The coffee-shop sign-in: a split page. An art panel carries the brand
   and the headline over the design's glow-and-grid ground; the form
   stands on the ground itself, with no card around it. The fields, the
   code step and the submit are the ones the page has always had — only
   the room around them is new. */
.login-app {
  display: grid;
  grid-template-columns: 1.1fr 1fr;
  min-height: 100vh;
  color: var(--ink);
}

/* ── the art panel ────────────────────────────────────────────────────── */
.login-art {
  position: relative;
  overflow: hidden;
  display: flex;
  flex-direction: column;
  justify-content: space-between;
  gap: 32px;
  padding: 48px;
  border-inline-end: 1px solid var(--line-soft);
  background:
    radial-gradient(600px 400px at 20% 20%, color-mix(in srgb, var(--accent) 28%, transparent), transparent 60%),
    radial-gradient(500px 400px at 80% 70%, color-mix(in srgb, var(--sky) 14%, transparent), transparent 60%),
    radial-gradient(400px 300px at 60% 30%, color-mix(in srgb, var(--accent) 20%, transparent), transparent 60%),
    var(--ground);
}
.login-art::after {
  content: '';
  position: absolute;
  inset: 0;
  background-image:
    linear-gradient(var(--fill-2) 1px, transparent 1px),
    linear-gradient(90deg, var(--fill-2) 1px, transparent 1px);
  background-size: 44px 44px;
  -webkit-mask-image: radial-gradient(ellipse at center, black 30%, transparent 75%);
  mask-image: radial-gradient(ellipse at center, black 30%, transparent 75%);
  pointer-events: none;
}
.login-art > * {
  position: relative;
  z-index: 1;
}
.art-brand {
  display: flex;
  align-items: center;
  gap: 12px;
}
.brand-logo {
  width: 40px;
  height: auto;
  display: block;
}
.art-wordmark {
  font-size: 22px;
  font-weight: 800;
  letter-spacing: -0.02em;
  color: var(--ink);
}
.art-headline {
  font-size: 46px;
  font-weight: 850;
  letter-spacing: -0.04em;
  line-height: 1.05;
}
.art-headline b {
  display: inline-block;
  font-weight: inherit;
  background: var(--primary);
  -webkit-background-clip: text;
  background-clip: text;
  color: transparent;
  animation: headline-in 280ms ease both;
}
@keyframes headline-in {
  0% { opacity: 0; transform: translateY(6px); }
  100% { opacity: 1; transform: translateY(0); }
}
.art-tagline {
  margin-top: 16px;
  max-width: 460px;
  color: var(--muted);
}
.art-foot {
  font-family: var(--mono);
  font-size: 11.5px;
  color: var(--faint);
}

/* ── the form ─────────────────────────────────────────────────────────── */
.login-content {
  display: grid;
  place-items: center;
  min-width: 0;
  padding: 40px 24px;
}
.login-box {
  width: min(400px, 100%);
}
.box-brand {
  display: none;
}
.box-head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 30px;
}
.box-head h2 {
  font-size: 26px;
  font-weight: 800;
  letter-spacing: -0.02em;
}
.box-sub {
  margin-top: 6px;
  color: var(--muted);
  font-size: 13px;
}
.box-tools {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  flex: none;
}
.tool-btn {
  display: inline-grid;
  place-items: center;
  width: 34px;
  height: 34px;
  padding: 0;
  border: 1px solid var(--line);
  border-radius: 10px;
  background: var(--surface);
  color: var(--muted);
  font-size: 16px;
  cursor: pointer;
}
.tool-btn:hover {
  color: var(--accent-text);
  border-color: var(--accent-line);
  background: var(--accent-soft);
}
.lang {
  position: relative;
}
.lang-menu {
  position: absolute;
  top: calc(100% + 6px);
  inset-inline-end: 0;
  z-index: 20;
  min-width: 160px;
}
.lang-menu .amenu-item.selected {
  color: var(--accent-text);
  background: var(--accent-soft);
}

@media (max-width: 900px) {
  .login-app {
    grid-template-columns: 1fr;
  }
  .login-art {
    display: none;
  }
  .box-brand {
    display: flex;
    align-items: center;
    gap: 10px;
    margin-bottom: 28px;
  }
  .box-brand .brand-logo {
    width: 34px;
  }
  .box-brand .art-wordmark {
    font-size: 19px;
  }
}
@media (prefers-reduced-motion: reduce) {
  .art-headline b {
    animation: none;
  }
}

/* Ant's Form layout="vertical" with size="large" controls. */
.login-form .aform-label {
  font-weight: 500;
  color: var(--ink);
}
.login-form .ainput.large {
  height: 42px;
  padding: 7px 11px;
  border-radius: var(--r-sm);
  font-size: 16px;
}
.login-form .ainput.large input {
  font-size: 16px;
}
.login-form .ainput-prefix {
  color: var(--faint);
}
.ainput-eye {
  display: inline-flex;
  padding: 0;
  border: 0;
  background: none;
  color: var(--faint);
  font-size: 14px;
  cursor: pointer;
}
.ainput-eye:hover {
  color: var(--ink);
}
.login-form input:-webkit-autofill,
.login-form input:-webkit-autofill:hover,
.login-form input:-webkit-autofill:focus {
  -webkit-text-fill-color: var(--ink) !important;
  -webkit-box-shadow: 0 0 0 1000px var(--surface-2) inset !important;
  box-shadow: 0 0 0 1000px var(--surface-2) inset !important;
  transition: background-color 9999s ease-in-out 0s, color 9999s ease-in-out 0s;
}
/* The code step, in its own calm card as the design sets it. */
.code-card {
  padding: 14px;
  border: 1px solid var(--line-soft);
  border-radius: var(--r);
  background: var(--surface);
}
.code-card .ainput.large {
  margin-bottom: 2px;
}
.abtn.large {
  height: 44px;
  padding: 6px 15px;
  border-radius: var(--r-sm);
  font-size: 15px;
}
.abtn.block {
  width: 100%;
}
.submit-row {
  margin-bottom: 0;
}
/* The passkey door: a quiet divider, then the same-sized button in the
   page's plain style -- the password stays the headline act. */
.passkey-block {
  margin-top: 20px;
}
.passkey-divider {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-bottom: 16px;
  color: var(--faint);
  font-size: 12px;
}
.passkey-divider::before,
.passkey-divider::after {
  content: '';
  flex: 1;
  border-top: 1px solid var(--line-soft);
}
/* A refusal is a banner, not a footnote: the design's bad tint, bordered. */
.error {
  margin: -8px 0 16px;
  padding: 10px 12px;
  border: 1px solid var(--bad-line);
  border-radius: 10px;
  background: var(--bad-soft);
  color: var(--bad);
  font-size: 13px;
}
.spin {
  width: 16px;
  height: 16px;
  border: 2px solid rgba(255, 255, 255, 0.35);
  border-top-color: #fff;
  border-radius: 50%;
  animation: rot 0.8s linear infinite;
}
@keyframes rot {
  to { transform: rotate(360deg); }
}
</style>
