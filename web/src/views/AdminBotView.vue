<script setup>
// The operator's own Telegram management bot: a second bot beside the
// sales one, with its own token, from which the operator runs the
// customers they already have -- listing, creating, cutting off,
// extending. It is a remote control, not a shop: nothing here is sold,
// and the only person it answers is the operator whose numeric ID is on
// file. Every operator runs their own, so this page is the caller's bot
// and nobody else's; the server answers it that way too.
import { computed, onMounted, ref } from 'vue'
import { api } from '../lib/api.js'
import { useDelayed } from '../lib/live.js'
import { t, notify } from '../lib/store.js'
import ErrorState from '../components/ErrorState.vue'
import PageSpin from '../components/PageSpin.vue'
import Toggle from '../components/Toggle.vue'

const loading = ref(true)
const loadError = ref(null)

// The config as the server last stated it (whether the bot is actually
// running, its username) and the form the operator edits. The token is
// never in either: the server says only whether one is on file, and the
// form's token field starts empty -- empty means "keep it", which is the
// only thing it can mean.
const cfg = ref(null)
// After cfg on purpose: useDelayed reads its source once when the watch
// is set up, so the computed may only name refs that already exist.
const showSkeleton = useDelayed(computed(() => loading.value && !cfg.value))
const form = ref({
  enabled: false,
  token: '',
  adminChatId: '',
})
const saving = ref(false)

onMounted(load)

function fillForm(c) {
  form.value = {
    enabled: !!c.enabled,
    token: '',
    adminChatId: c.adminChatId ? String(c.adminChatId) : '',
  }
}

async function load() {
  loading.value = true
  try {
    // The config is the whole page: there is nothing around it that could
    // be allowed to fail separately, the way the shop's summaries are.
    const c = await api.adminBotConfig()
    cfg.value = c
    fillForm(c)
    loadError.value = null
  } catch (e) {
    loadError.value = e
  } finally {
    loading.value = false
  }
}

async function saveConfig() {
  saving.value = true
  try {
    const body = {
      enabled: form.value.enabled,
      adminChatId: Number(form.value.adminChatId) || 0,
    }
    // Sent only when a new one was typed. An empty field cannot mean
    // "clear the token" -- there is a bot running on it -- so it means
    // "keep", and the way to say that is to leave it out.
    if (form.value.token.trim()) body.token = form.value.token.trim()
    await api.adminBotConfigSave(body)
    // Read the config back rather than trusting what the save answered:
    // whether the bot took the token and came up is the server's to say,
    // and the status line above is drawn from exactly that.
    const fresh = await api.adminBotConfig().catch(() => null)
    if (fresh) {
      cfg.value = fresh
      fillForm(fresh)
    } else {
      form.value.token = ''
    }
    notify(t('adminbot.saved'))
  } catch (e) {
    notify(e.message, 'error')
  } finally {
    saving.value = false
  }
}

// The bot's state as the server last saw it, not as the unsaved switch
// wishes it were: a switch just flipped has not connected anything yet.
const botStatus = computed(() => {
  if (!cfg.value?.enabled) return { text: t('adminbot.off'), tone: 'grey' }
  if (cfg.value.botRunning) return { text: t('adminbot.connected'), tone: 'green' }
  return { text: t('adminbot.connecting'), tone: 'orange' }
})

// What the bot does, in the order an operator reaches for it. A guide,
// not settings: it is here so the page says what the bot is for before
// the operator goes and makes one in BotFather.
const guideItems = [
  'adminbot.guide.list',
  'adminbot.guide.create',
  'adminbot.guide.toggle',
  'adminbot.guide.extend',
  'adminbot.guide.reset',
  'adminbot.guide.delete',
  'adminbot.guide.stats',
]
</script>

<template>
  <PageSpin v-if="showSkeleton" />
  <div v-else-if="loading" class="empty"></div>
  <ErrorState v-else-if="loadError" :error="loadError" @retry="load" />

  <div v-else class="adminbot-stack">
    <header class="page-head">
      <div>
        <h1>{{ t('nav.adminBot') }}</h1>
        <p>{{ t('adminbot.intro') }}</p>
      </div>
    </header>

    <!-- The bot's whole config is three fields, so it is one card with
         one save, the way the sales bot's is: two saves would leave the
         operator guessing which half a failure belonged to. -->
    <form class="card" @submit.prevent="saveConfig">
      <div class="card-head">
        <h2>{{ t('adminbot.title') }}</h2>
        <span class="tag num" :class="botStatus.tone">{{ botStatus.text }}</span>
        <span v-if="cfg?.botUsername" class="muted small ltr">@{{ cfg.botUsername }}</span>
      </div>
      <div class="card-body adminbot-form">
        <div class="setting-list-item"><div class="arow">
          <div class="acol"><div class="setting-list-meta">
            <div class="setting-list-title">{{ t('adminbot.enable') }}</div>
            <div class="setting-list-description">{{ t('adminbot.enableDesc') }}</div>
          </div></div>
          <div class="acol"><Toggle v-model="form.enabled" :label="t('adminbot.enable')" /></div>
        </div></div>

        <div class="setting-list-item"><div class="arow">
          <div class="acol"><div class="setting-list-meta">
            <div class="setting-list-title">{{ t('adminbot.token') }}</div>
            <div class="setting-list-description">{{ cfg?.tokenSet ? t('adminbot.tokenSet') : t('adminbot.tokenPlaceholder') }}</div>
          </div></div>
          <div class="acol">
            <label class="ainput block"><input v-model="form.token" type="password" autocomplete="off" spellcheck="false" class="ltr" :placeholder="cfg?.tokenSet ? t('adminbot.tokenSet') : t('adminbot.tokenPlaceholder')" /></label>
          </div>
        </div></div>

        <div class="setting-list-item"><div class="arow">
          <div class="acol"><div class="setting-list-meta">
            <div class="setting-list-title">{{ t('adminbot.adminChatId') }}</div>
            <div class="setting-list-description">{{ t('adminbot.adminChatIdDesc') }}</div>
          </div></div>
          <div class="acol">
            <label class="ainput block"><input v-model="form.adminChatId" type="number" inputmode="numeric" class="ltr" placeholder="123456789" /></label>
          </div>
        </div></div>

        <div class="adminbot-save">
          <button type="submit" class="btn primary" :disabled="saving">
            <span v-if="saving" class="spin sm"></span>
            <span>{{ t('action.save') }}</span>
          </button>
        </div>
      </div>
    </form>

    <!-- What the bot does once it is on. Read before BotFather, so the
         operator knows what they are making -- and that it is a second
         bot, not the sales one with a new hat. -->
    <div class="card">
      <div class="card-head">
        <h2>{{ t('adminbot.guide.title') }}</h2>
      </div>
      <div class="card-body">
        <ul class="adminbot-guide">
          <li v-for="key in guideItems" :key="key">{{ t(key) }}</li>
        </ul>
        <p class="muted small">{{ t('adminbot.guide.note') }}</p>
      </div>
    </div>
  </div>
</template>

<style scoped>
.adminbot-stack {
  display: flex;
  flex-direction: column;
  gap: 18px;
}
.adminbot-form {
  display: flex;
  flex-direction: column;
}
.adminbot-save {
  display: flex;
  justify-content: flex-end;
  margin-top: 16px;
}
/* The guide is a plain list: what the bot does, one thing per line, in
   the muted voice the panel uses for things that explain rather than ask. */
.adminbot-guide {
  margin: 0 0 12px;
  padding-inline-start: 20px;
  display: flex;
  flex-direction: column;
  gap: 6px;
  color: var(--muted);
  font-size: var(--t-sm);
}
</style>
