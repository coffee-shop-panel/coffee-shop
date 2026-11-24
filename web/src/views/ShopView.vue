<script setup>
// The operator's own Telegram sales bot: its switch and token, the ways it
// can be paid, the plans it sells and the orders it has taken. Every
// operator on this panel -- owner, admin, reseller -- runs their own bot
// with their own token and their own card, so everything on this page is
// the caller's shop and nobody else's; the server answers it that way too.
import { computed, onMounted, ref } from 'vue'
import { api } from '../lib/api.js'
import { useDelayed } from '../lib/live.js'
import { store, t, notify } from '../lib/store.js'
import { bytes, bytesToGigabytes, gigabytesToBytes, dateTime } from '../lib/format.js'
import ErrorState from '../components/ErrorState.vue'
import PageSpin from '../components/PageSpin.vue'
import Toggle from '../components/Toggle.vue'
import MultiSelect from '../components/MultiSelect.vue'
import ConfirmDialog from '../components/ConfirmDialog.vue'
import Icon from '../components/Icon.vue'

const loading = ref(true)
const loadError = ref(null)

// The config as the server last stated it (whether the bot is actually
// running, its username, whether the public address may be edited) and the
// form the operator edits. The token is never in either: the server says
// only whether one is on file, and the form's token field starts empty --
// empty means "keep it", which is the only thing it can mean.
const cfg = ref(null)
// After cfg on purpose: useDelayed reads its source once when the watch
// is set up, so the computed may only name refs that already exist.
const showSkeleton = useDelayed(computed(() => loading.value && !cfg.value))
const form = ref({
  enabled: false,
  token: '',
  adminChatId: '',
  cardNumber: '',
  cardHolder: '',
  zarinpalMerchant: '',
  welcome: '',
  interfaceIds: [],
  publicBase: '',
  // The v5 settings ride in the same form and the same save: the trial
  // (its quota typed in gigabytes, like a plan's), the naming pattern,
  // the cashback percent, the wheel's switch, and the shop's own
  // tutorial and FAQ words.
  trialEnabled: false,
  trialGb: '',
  trialDays: '',
  trialSection: 'wireguard',
  nameFormat: '',
  cashbackPercent: '',
  wheelEnabled: false,
  tutorialText: '',
  faqText: '',
})
const saving = ref(false)

const stats = ref(null)
const plans = ref([])
const codes = ref([])
const orders = ref([])
const buyers = ref([])
const interfaces = ref([])

// Orders open on the ones waiting for a decision: that is what the seller
// comes to this page to clear.
const orderFilter = ref('pending_review')
const orderBusy = ref(0)
// The link an approval just produced, kept on screen until dismissed: the
// bot delivers it to the buyer itself, but a seller whose buyer cannot be
// reached needs it to pass on by hand.
const delivered = ref(null)

const planEdit = ref(null)
const planBusy = ref(false)
const planDelete = ref(null)
const codeEdit = ref(null)
const codeBusy = ref(false)
const codeDelete = ref(null)
const orderReject = ref(null)

const nf = (n) => Number(n || 0).toLocaleString(store.locale)
const money = (n) => `${nf(n)} ${t('shop.toman')}`

onMounted(load)

function fillForm(c) {
  form.value = {
    enabled: !!c.enabled,
    token: '',
    adminChatId: c.adminChatId ? String(c.adminChatId) : '',
    cardNumber: c.cardNumber || '',
    cardHolder: c.cardHolder || '',
    zarinpalMerchant: c.zarinpalMerchant || '',
    welcome: c.welcome || '',
    interfaceIds: [...(c.interfaceIds || [])],
    publicBase: c.publicBase || '',
    trialEnabled: !!c.trialEnabled,
    trialGb: bytesToGigabytes(c.trialBytes),
    trialDays: c.trialDays || '',
    trialSection: c.trialSection === 'v2ray' ? 'v2ray' : 'wireguard',
    nameFormat: c.nameFormat || '',
    cashbackPercent: c.cashbackPercent || '',
    wheelEnabled: !!c.wheelEnabled,
    tutorialText: c.tutorialText || '',
    faqText: c.faqText || '',
  }
}

async function load() {
  loading.value = true
  try {
    // The config is the page; the rest are summaries around it, and each is
    // allowed to fail without taking the page down with it.
    const [c, p, s, ifaces, b, cd] = await Promise.all([
      api.shopConfig(),
      api.shopPlans().catch(() => null),
      api.shopStats().catch(() => null),
      api.interfaces().catch(() => []),
      api.shopBuyers().catch(() => null),
      api.shopCodes().catch(() => null),
    ])
    cfg.value = c
    fillForm(c)
    plans.value = p?.items || []
    codes.value = cd?.items || []
    stats.value = s
    interfaces.value = ifaces || []
    buyers.value = b?.items || []
    loadError.value = null
    await loadOrders()
  } catch (e) {
    loadError.value = e
  } finally {
    loading.value = false
  }
}

async function loadOrders() {
  try {
    const res = await api.shopOrders(orderFilter.value)
    orders.value = res?.items || []
  } catch (e) {
    notify(e.message, 'error')
  }
}

async function refreshStats() {
  try {
    stats.value = await api.shopStats()
  } catch {
    /* the counters are a summary, not the work: a stale one is no reason
       to report a failure over an action that succeeded */
  }
}

// One save for the bot and the payment methods together: they are one
// config on the server, and two saves would leave the operator guessing
// which half a failure belonged to.
async function saveConfig() {
  saving.value = true
  try {
    const body = {
      enabled: form.value.enabled,
      adminChatId: Number(form.value.adminChatId) || 0,
      cardNumber: form.value.cardNumber.trim(),
      cardHolder: form.value.cardHolder.trim(),
      zarinpalMerchant: form.value.zarinpalMerchant.trim(),
      welcome: form.value.welcome,
      interfaceIds: form.value.interfaceIds,
      trialEnabled: !!form.value.trialEnabled,
      trialBytes: gigabytesToBytes(form.value.trialGb),
      trialDays: Math.max(0, Math.round(Number(form.value.trialDays) || 0)),
      trialSection: form.value.trialSection,
      nameFormat: form.value.nameFormat,
      cashbackPercent: Math.min(100, Math.max(0, Math.round(Number(form.value.cashbackPercent) || 0))),
      wheelEnabled: !!form.value.wheelEnabled,
      tutorialText: form.value.tutorialText,
      faqText: form.value.faqText,
    }
    // Sent only when a new one was typed. An empty field cannot mean
    // "clear the token" -- there is a bot running on it -- so it means
    // "keep", and the way to say that is to leave it out.
    if (form.value.token.trim()) body.token = form.value.token.trim()
    // The public address is the owner's alone to set; the field is only
    // offered where the server said it may be edited.
    if (cfg.value?.publicBaseEditable) body.publicBase = form.value.publicBase.trim()
    await api.shopConfigSave(body)
    // Read the config back rather than trusting what the save answered:
    // whether the bot took the token and came up is the server's to say,
    // and the status line above is drawn from exactly that.
    const fresh = await api.shopConfig().catch(() => null)
    if (fresh) {
      cfg.value = fresh
      fillForm(fresh)
    } else {
      form.value.token = ''
    }
    notify(t('shop.saved'))
  } catch (e) {
    notify(e.message, 'error')
  } finally {
    saving.value = false
  }
}

// The bot's state as the server last saw it, not as the unsaved switch
// wishes it were: a switch just flipped has not connected anything yet.
const botStatus = computed(() => {
  if (!cfg.value?.enabled) return { text: t('shop.bot.off'), tone: 'gray' }
  if (cfg.value.botRunning) return { text: t('shop.bot.connected'), tone: 'ok' }
  return { text: t('shop.bot.connecting'), tone: 'warn' }
})

// The same option shape the customer form's server picker uses, over every
// interface this operator may sell: the bot offers what is picked here and
// nothing else.
const serverOptions = computed(() =>
  interfaces.value.map((i) => ({
    value: i.id,
    label: i.name,
    tags: [
      { text: t(`protocol.${i.protocol}`), kind: 'proto' },
      ...(i.mode === 'amnezia' ? [{ text: 'AmneziaWG' }] : []),
    ],
    note: i.capacity ? (i.capacity - i.allocated).toLocaleString() : '',
  })),
)

const statTiles = computed(() => {
  const s = stats.value
  if (!s) return []
  return [
    { key: 'today', label: t('shop.stats.today'), n: s.todaySales, value: nf(s.todaySales) },
    { key: 'total', label: t('shop.stats.total'), n: s.totalSales, value: nf(s.totalSales) },
    { key: 'revenue', label: t('shop.stats.revenue'), n: s.revenueToman, value: money(s.revenueToman) },
    { key: 'pending', label: t('shop.stats.pending'), n: s.pending, value: nf(s.pending), urgent: s.pending > 0 },
    { key: 'buyers', label: t('shop.stats.buyers'), n: s.buyers, value: nf(s.buyers) },
  ]
})

// ── plans ────────────────────────────────────────────────────────────────

// Every plan, and every order taken from one, belongs to one of the two
// shelves the bot sells from: WireGuard or V2ray. A plan made before the
// shelves existed carries no section; it is a WireGuard plan, which is
// what the bot sold then and what the server still calls it.
const sectionOf = (x) => (x?.section === 'v2ray' ? 'v2ray' : 'wireguard')
const SECTION_TONES = { wireguard: 'info', v2ray: 'purple' }

function openPlan(plan) {
  planEdit.value = plan
    ? {
        id: plan.id,
        name: plan.name || '',
        section: sectionOf(plan),
        quotaGb: bytesToGigabytes(plan.quotaBytes),
        days: plan.days || '',
        priceToman: plan.priceToman ?? '',
        deviceLimit: plan.deviceLimit ?? '',
        enabled: !!plan.enabled,
        sort: plan.sort ?? '',
      }
    : { id: 0, name: '', section: 'wireguard', quotaGb: '', days: '', priceToman: '', deviceLimit: '', enabled: true, sort: '' }
}

const planValid = computed(() => {
  const p = planEdit.value
  if (!p) return false
  return p.name.trim() !== '' && Number(p.days) > 0 && Number(p.priceToman) >= 0 && p.priceToman !== ''
})

async function savePlan() {
  const p = planEdit.value
  if (!p || !planValid.value) return
  planBusy.value = true
  try {
    await api.shopPlanSave({
      id: p.id || 0,
      name: p.name.trim(),
      section: p.section,
      quotaBytes: gigabytesToBytes(p.quotaGb),
      days: Math.round(Number(p.days)),
      priceToman: Math.round(Number(p.priceToman)),
      deviceLimit: Math.max(0, Math.round(Number(p.deviceLimit) || 0)),
      enabled: !!p.enabled,
      sort: Math.round(Number(p.sort) || 0),
    })
    const res = await api.shopPlans().catch(() => null)
    if (res) plans.value = res.items || []
    planEdit.value = null
    notify(t('shop.plans.saved'))
  } catch (e) {
    notify(e.message, 'error')
  } finally {
    planBusy.value = false
  }
}

async function deletePlan() {
  const plan = planDelete.value
  if (!plan) return
  planBusy.value = true
  try {
    await api.shopPlanDelete(plan.id)
    plans.value = plans.value.filter((p) => p.id !== plan.id)
    planDelete.value = null
    notify(t('shop.plans.deleted'))
  } catch (e) {
    notify(e.message, 'error')
  } finally {
    planBusy.value = false
  }
}

// ── codes ────────────────────────────────────────────────────────────────

// The codes a buyer names: percent and fixed ones take money off a
// checkout, a gift one is redeemed into a wallet. The expiry is edited
// as a plain date -- empty is a code that never runs out -- and sent
// as the end of that day, so a code said to end on a date is good for
// all of it.
const CODE_KINDS = ['percent', 'fixed', 'gift']
const kindOf = (c) => (CODE_KINDS.includes(c?.kind) ? c.kind : 'percent')

function openCode(code) {
  codeEdit.value = code
    ? {
        id: code.id,
        code: code.code || '',
        kind: kindOf(code),
        value: code.value ?? '',
        maxUses: code.maxUses ?? '',
        expiresDate: code.expiresAt ? String(code.expiresAt).slice(0, 10) : '',
        enabled: !!code.enabled,
      }
    : { id: 0, code: '', kind: 'percent', value: '', maxUses: '', expiresDate: '', enabled: true }
}

const codeValid = computed(() => {
  const c = codeEdit.value
  if (!c) return false
  return c.code.trim() !== '' && Number(c.value) > 0
})

async function saveCode() {
  const c = codeEdit.value
  if (!c || !codeValid.value) return
  codeBusy.value = true
  try {
    await api.shopCodeSave({
      id: c.id || 0,
      code: c.code.trim(),
      kind: c.kind,
      value: Math.round(Number(c.value)),
      maxUses: Math.max(0, Math.round(Number(c.maxUses) || 0)),
      expiresAt: c.expiresDate ? new Date(`${c.expiresDate}T23:59:59.999Z`).toISOString() : null,
      enabled: !!c.enabled,
    })
    const res = await api.shopCodes().catch(() => null)
    if (res) codes.value = res.items || []
    codeEdit.value = null
    notify(t('shop.codes.saved'))
  } catch (e) {
    notify(e.message, 'error')
  } finally {
    codeBusy.value = false
  }
}

async function deleteCode() {
  const code = codeDelete.value
  if (!code) return
  codeBusy.value = true
  try {
    await api.shopCodeDelete(code.id)
    codes.value = codes.value.filter((c) => c.id !== code.id)
    codeDelete.value = null
    notify(t('shop.codes.deleted'))
  } catch (e) {
    notify(e.message, 'error')
  } finally {
    codeBusy.value = false
  }
}

// ── orders ───────────────────────────────────────────────────────────────

const STATUS_TONES = {
  unpaid: 'gray',
  awaiting_receipt: 'gray',
  pending_review: 'warn',
  paid: 'info',
  fulfilled: 'ok',
  rejected: 'bad',
  expired: 'gray',
  failed: 'bad',
}

// A buyer's standing, in the design's tag words.
const BUYER_TONES = { active: 'ok', disabled: 'gray', expired: 'bad', depleted: 'warn', limited: 'warn' }
const actionable = (o) => o.status === 'pending_review' || o.status === 'paid'

async function approveOrder(order) {
  orderBusy.value = order.id
  try {
    const res = await api.shopOrderApprove(order.id)
    notify(t('shop.orders.approved'))
    if (res?.subLink) delivered.value = { link: res.subLink, name: order.buyerName || '' }
    await loadOrders()
    refreshStats()
  } catch (e) {
    notify(e.message, 'error')
  } finally {
    orderBusy.value = 0
  }
}

async function rejectOrder() {
  const order = orderReject.value
  if (!order) return
  orderBusy.value = order.id
  try {
    await api.shopOrderReject(order.id)
    orderReject.value = null
    notify(t('shop.orders.rejected'))
    await loadOrders()
    refreshStats()
  } catch (e) {
    notify(e.message, 'error')
  } finally {
    orderBusy.value = 0
  }
}

async function copyLink() {
  try {
    await navigator.clipboard.writeText(delivered.value.link)
    notify(t('action.copied'))
  } catch {
    notify(t('action.copyFailed'), 'error')
  }
}

// ── receipts ─────────────────────────────────────────────────────────────

// A card order's receipt photo, open on screen: { order, url }, the url
// an object address over the fetched bytes. It is revoked when the modal
// closes -- left alone, every viewing would keep its photo in memory.
const receiptView = ref(null)

async function viewReceipt(order) {
  try {
    const blob = await api.shopReceipt(order.id)
    receiptView.value = { order, url: URL.createObjectURL(blob) }
  } catch (e) {
    notify(e.message, 'error')
  }
}

function closeReceipt() {
  if (receiptView.value) URL.revokeObjectURL(receiptView.value.url)
  receiptView.value = null
}

async function approveReceipt() {
  const order = receiptView.value?.order
  if (!order) return
  await approveOrder(order)
  closeReceipt()
}

// Rejecting keeps its confirmation: the receipt closes and the usual
// question takes its place, so there is one reject path, not two.
function rejectReceipt() {
  const order = receiptView.value?.order
  closeReceipt()
  if (order) orderReject.value = order
}

// ── buyers & announcements ───────────────────────────────────────────────

// The announcement's audience: 0 is every buyer the bot has made, and
// anything else is one buyer's Telegram id -- picked in the card below,
// or set from that buyer's own row, which is the usual way in.
const announce = ref({ target: 0, text: '' })
const announceBusy = ref(false)
// The bot has to be running to carry a word anywhere: with it stopped,
// sending is not offered and the card says why, rather than the seller
// learning it from the silence that follows.
const announceReady = computed(() => !!cfg.value?.botRunning && announce.value.text.trim() !== '')

function openAnnounce(buyer) {
  announce.value.target = buyer ? buyer.telegramId : 0
  document.getElementById('shop-announce')?.scrollIntoView({ behavior: 'smooth', block: 'start' })
}

async function sendAnnounce() {
  const text = announce.value.text.trim()
  if (!text || announceBusy.value) return
  announceBusy.value = true
  try {
    const body = { text }
    if (announce.value.target) body.buyerTgId = announce.value.target
    const res = await api.shopAnnounce(body)
    notify(t('shop.announce.sent', { count: res?.sent ?? 0 }))
    announce.value.text = ''
  } catch (e) {
    notify(e.message, 'error')
  } finally {
    announceBusy.value = false
  }
}
</script>

<template>
  <PageSpin v-if="showSkeleton" />
  <div v-else-if="loading" class="empty"></div>
  <ErrorState v-else-if="loadError" :error="loadError" @retry="load" />

  <div v-else class="shop-stack cs">
    <div class="ph">
      <div>
        <h2>{{ t('nav.shop') }}</h2>
        <p>{{ t('shop.lede') }}</p>
      </div>
    </div>
    <!-- The shop at a glance, once the counters have loaded: what sold
         today, what has sold altogether, and what is waiting on the
         seller -- the one number here that asks for something. -->
    <div v-if="statTiles.length" class="grid shop-stats">
      <div v-for="tile in statTiles" :key="tile.key" class="card stat" :class="{ quiet: !tile.n, hot: tile.urgent }">
        <span class="lbl">{{ tile.label }}</span>
        <div class="val num">{{ tile.value }}</div>
      </div>
    </div>

    <!-- The bot and how it is paid are one config with one save, so they
         are one card with two halves rather than two cards whose saves
         could disagree about what was sent. -->
    <form class="card" @submit.prevent="saveConfig">
      <div class="card-head">
        <h2>{{ t('shop.bot.title') }}</h2>
        <span class="dtag" :class="botStatus.tone"><i class="dot"></i>{{ botStatus.text }}</span>
        <span v-if="cfg?.botUsername" class="muted small ltr">@{{ cfg.botUsername }}</span>
      </div>
      <div class="card-body shop-form">
        <div class="setting-list-item"><div class="arow">
          <div class="acol"><div class="setting-list-meta">
            <div class="setting-list-title">{{ t('shop.bot.enable') }}</div>
            <div class="setting-list-description">{{ t('shop.bot.enableDesc') }}</div>
          </div></div>
          <div class="acol"><Toggle v-model="form.enabled" :label="t('shop.bot.enable')" /></div>
        </div></div>

        <div class="setting-list-item"><div class="arow">
          <div class="acol"><div class="setting-list-meta">
            <div class="setting-list-title">{{ t('shop.bot.token') }}</div>
            <div class="setting-list-description">{{ cfg?.tokenSet ? t('shop.bot.tokenSet') : t('shop.bot.tokenPlaceholder') }}</div>
          </div></div>
          <div class="acol">
            <label class="ainput block"><input v-model="form.token" type="password" autocomplete="off" spellcheck="false" class="ltr" :placeholder="cfg?.tokenSet ? t('shop.bot.tokenSet') : t('shop.bot.tokenPlaceholder')" /></label>
          </div>
        </div></div>

        <div class="setting-list-item"><div class="arow">
          <div class="acol"><div class="setting-list-meta">
            <div class="setting-list-title">{{ t('shop.bot.adminChatId') }}</div>
            <div class="setting-list-description">{{ t('shop.bot.adminChatIdDesc') }}</div>
          </div></div>
          <div class="acol">
            <label class="ainput block"><input v-model="form.adminChatId" type="number" inputmode="numeric" class="ltr" placeholder="123456789" /></label>
          </div>
        </div></div>

        <div class="setting-list-item"><div class="arow">
          <div class="acol"><div class="setting-list-meta">
            <div class="setting-list-title">{{ t('shop.bot.welcome') }}</div>
            <div class="setting-list-description">{{ t('shop.bot.welcomeHint') }}</div>
          </div></div>
          <div class="acol">
            <label class="ainput block"><textarea v-model="form.welcome" rows="3"></textarea></label>
          </div>
        </div></div>

        <div class="setting-list-item"><div class="arow">
          <div class="acol"><div class="setting-list-meta">
            <div class="setting-list-title">{{ t('shop.bot.interfaces') }}</div>
          </div></div>
          <div class="acol">
            <MultiSelect v-model="form.interfaceIds" :options="serverOptions" :placeholder="t('shop.bot.interfacesPlaceholder')" />
          </div>
        </div></div>
      </div>

      <div class="card-head shop-subhead">
        <h2>{{ t('shop.pay.title') }}</h2>
      </div>
      <div class="card-body shop-form">
        <div class="setting-list-item"><div class="arow">
          <div class="acol"><div class="setting-list-meta">
            <div class="setting-list-title">{{ t('shop.pay.cardNumber') }}</div>
          </div></div>
          <div class="acol">
            <label class="ainput block"><input v-model="form.cardNumber" inputmode="numeric" autocomplete="off" class="ltr" placeholder="6037-…………" /></label>
          </div>
        </div></div>

        <div class="setting-list-item"><div class="arow">
          <div class="acol"><div class="setting-list-meta">
            <div class="setting-list-title">{{ t('shop.pay.cardHolder') }}</div>
          </div></div>
          <div class="acol">
            <label class="ainput block"><input v-model="form.cardHolder" autocomplete="off" /></label>
          </div>
        </div></div>

        <div class="setting-list-item"><div class="arow">
          <div class="acol"><div class="setting-list-meta">
            <div class="setting-list-title">{{ t('shop.pay.zarinpal') }}</div>
            <div class="setting-list-description">{{ t('shop.pay.zarinpalHint') }}</div>
          </div></div>
          <div class="acol">
            <label class="ainput block"><input v-model="form.zarinpalMerchant" autocomplete="off" spellcheck="false" class="ltr" /></label>
          </div>
        </div></div>

        <div v-if="cfg?.publicBaseEditable" class="setting-list-item"><div class="arow">
          <div class="acol"><div class="setting-list-meta">
            <div class="setting-list-title">{{ t('shop.pay.publicBase') }}</div>
            <div class="setting-list-description">{{ t('shop.pay.publicBaseHint') }}</div>
          </div></div>
          <div class="acol">
            <label class="ainput block"><input v-model="form.publicBase" autocomplete="off" spellcheck="false" class="ltr" placeholder="https://…:12489" /></label>
          </div>
        </div></div>
        <p v-else-if="cfg?.publicBase" class="muted small">
          {{ t('shop.pay.publicBase') }}: <span class="ltr">{{ cfg.publicBase }}</span>
        </p>
      </div>

      <div class="card-head shop-subhead">
        <h2>{{ t('shop.v5.title') }}</h2>
      </div>
      <div class="card-body shop-form">
        <div class="setting-list-item"><div class="arow">
          <div class="acol"><div class="setting-list-meta">
            <div class="setting-list-title">{{ t('shop.v5.trial') }}</div>
            <div class="setting-list-description">{{ t('shop.v5.trialDesc') }}</div>
          </div></div>
          <div class="acol"><Toggle v-model="form.trialEnabled" :label="t('shop.v5.trial')" /></div>
        </div></div>

        <div class="shop-grid">
          <div class="field">
            <label for="sv5-trial-quota">{{ t('shop.plans.quotaGb') }}</label>
            <input id="sv5-trial-quota" v-model="form.trialGb" type="number" min="0" step="any" inputmode="decimal" class="ltr" placeholder="0" />
          </div>
          <div class="field">
            <label for="sv5-trial-days">{{ t('shop.plans.days') }}</label>
            <input id="sv5-trial-days" v-model="form.trialDays" type="number" min="0" step="1" inputmode="numeric" class="ltr" placeholder="0" />
          </div>
          <div class="field">
            <label for="sv5-trial-section">{{ t('shop.plans.section') }}</label>
            <div class="aselect">
              <select id="sv5-trial-section" v-model="form.trialSection">
                <option value="wireguard">{{ t('shop.section.wireguard') }}</option>
                <option value="v2ray">{{ t('shop.section.v2ray') }}</option>
              </select>
            </div>
          </div>
          <div class="field">
            <label for="sv5-cashback">{{ t('shop.v5.cashback') }}</label>
            <input id="sv5-cashback" v-model="form.cashbackPercent" type="number" min="0" max="100" step="1" inputmode="numeric" class="ltr" placeholder="0" />
            <span class="hint">{{ t('shop.v5.cashbackDesc') }}</span>
          </div>
        </div>

        <div class="setting-list-item"><div class="arow">
          <div class="acol"><div class="setting-list-meta">
            <div class="setting-list-title">{{ t('shop.v5.wheel') }}</div>
            <div class="setting-list-description">{{ t('shop.v5.wheelDesc') }}</div>
          </div></div>
          <div class="acol"><Toggle v-model="form.wheelEnabled" :label="t('shop.v5.wheel')" /></div>
        </div></div>

        <div class="field">
          <label for="sv5-name-format">{{ t('shop.v5.nameFormat') }}</label>
          <input id="sv5-name-format" v-model="form.nameFormat" type="text" autocomplete="off" spellcheck="false" class="ltr" />
          <span class="hint">{{ t('shop.v5.nameFormatHint') }}</span>
        </div>

        <div class="field">
          <label for="sv5-tutorial">{{ t('shop.v5.tutorial') }}</label>
          <textarea id="sv5-tutorial" v-model="form.tutorialText" rows="4"></textarea>
          <span class="hint">{{ t('shop.v5.tutorialHint') }}</span>
        </div>

        <div class="field">
          <label for="sv5-faq">{{ t('shop.v5.faq') }}</label>
          <textarea id="sv5-faq" v-model="form.faqText" rows="4"></textarea>
          <span class="hint">{{ t('shop.v5.faqHint') }}</span>
        </div>

        <div class="shop-save">
          <button type="submit" class="btn primary" :disabled="saving">
            <span v-if="saving" class="spin sm"></span>
            <span>{{ t('action.save') }}</span>
          </button>
        </div>
      </div>
    </form>

    <!-- The plans the bot offers, in the order it offers them. -->
    <div class="card">
      <div class="card-head">
        <h2>{{ t('shop.plans.title') }}</h2>
        <button class="btn sm primary spacer" @click="openPlan(null)">
          <Icon name="plus" :size="14" /><span>{{ t('shop.plans.add') }}</span>
        </button>
      </div>
      <div class="tw">
        <table>
          <thead>
            <tr>
              <th>{{ t('shop.plans.name') }}</th>
              <th>{{ t('shop.plans.section') }}</th>
              <th>{{ t('shop.plans.quotaGb') }}</th>
              <th>{{ t('shop.plans.days') }}</th>
              <th>{{ t('shop.plans.price') }}</th>
              <th>{{ t('shop.plans.deviceLimit') }}</th>
              <th>{{ t('shop.plans.enabled') }}</th>
              <th class="w-sm"></th>
            </tr>
          </thead>
          <tbody>
            <tr v-if="!plans.length" class="empty-row">
              <td colspan="8">
                <div class="card-empty"><div>{{ t('shop.plans.empty') }}</div></div>
              </td>
            </tr>
            <tr v-for="p in plans" :key="p.id">
              <td>{{ p.name }}</td>
              <td><span class="dtag" :class="SECTION_TONES[sectionOf(p)]">{{ t(`shop.section.${sectionOf(p)}`) }}</span></td>
              <td class="num">{{ p.quotaBytes ? bytes(p.quotaBytes, store.locale) : t('shop.plans.unlimited') }}</td>
              <td class="num">{{ nf(p.days) }} {{ t('unit.days') }}</td>
              <td class="num">{{ money(p.priceToman) }}</td>
              <td class="num">{{ p.deviceLimit ? nf(p.deviceLimit) : t('shop.plans.unlimited') }}</td>
              <td><span class="dtag" :class="p.enabled ? 'ok' : 'gray'">{{ p.enabled ? t('shop.plans.enabled') : t('shop.bot.off') }}</span></td>
              <td>
                <div class="row">
                  <button class="btn sm ghost icon" :title="t('action.edit')" @click="openPlan(p)"><Icon name="edit" :size="14" /></button>
                  <button class="btn sm ghost icon" :title="t('action.delete')" @click="planDelete = p"><Icon name="trash" :size="14" /></button>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <!-- The codes a buyer can name: discounts at checkout, gifts into
         a wallet. A code's uses are counted by the store itself; this
         table only shows the count it keeps. -->
    <div class="card">
      <div class="card-head">
        <h2>{{ t('shop.codes.title') }}</h2>
        <button class="btn sm primary spacer" @click="openCode(null)">
          <Icon name="plus" :size="14" /><span>{{ t('shop.codes.add') }}</span>
        </button>
      </div>
      <div class="tw">
        <table>
          <thead>
            <tr>
              <th>{{ t('shop.codes.code') }}</th>
              <th>{{ t('shop.codes.kind') }}</th>
              <th>{{ t('shop.codes.value') }}</th>
              <th>{{ t('shop.codes.used') }} / {{ t('shop.codes.maxUses') }}</th>
              <th>{{ t('shop.codes.expires') }}</th>
              <th>{{ t('shop.plans.enabled') }}</th>
              <th class="w-sm"></th>
            </tr>
          </thead>
          <tbody>
            <tr v-if="!codes.length" class="empty-row">
              <td colspan="7">
                <div class="card-empty"><div>{{ t('shop.codes.empty') }}</div></div>
              </td>
            </tr>
            <tr v-for="c in codes" :key="c.id">
              <td class="ltr">{{ c.code }}</td>
              <td><span class="dtag acc">{{ t(`shop.codes.kind.${kindOf(c)}`) }}</span></td>
              <td class="num">{{ kindOf(c) === 'percent' ? `${nf(c.value)}%` : money(c.value) }}</td>
              <td class="num">{{ nf(c.usedCount) }} / {{ c.maxUses ? nf(c.maxUses) : t('shop.plans.unlimited') }}</td>
              <td class="small muted">{{ c.expiresAt ? dateTime(c.expiresAt, store.locale) : t('shop.codes.noExpiry') }}</td>
              <td><span class="dtag" :class="c.enabled ? 'ok' : 'gray'">{{ c.enabled ? t('shop.plans.enabled') : t('shop.bot.off') }}</span></td>
              <td>
                <div class="row">
                  <button class="btn sm ghost icon" :title="t('action.edit')" @click="openCode(c)"><Icon name="edit" :size="14" /></button>
                  <button class="btn sm ghost icon" :title="t('action.delete')" @click="codeDelete = c"><Icon name="trash" :size="14" /></button>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <!-- The orders, opened on the ones waiting for a decision. Approving
         one is its delivery: the customer is made on the spot. -->
    <div class="card">
      <div class="card-head">
        <h2>{{ t('shop.orders.title') }}</h2>
        <div class="aselect spacer">
          <select v-model="orderFilter" :aria-label="t('shop.orders.status')" @change="loadOrders">
            <option value="pending_review">{{ t('shop.status.pending_review') }}</option>
            <option value="">{{ t('common.all') }}</option>
            <option value="paid">{{ t('shop.status.paid') }}</option>
            <option value="fulfilled">{{ t('shop.status.fulfilled') }}</option>
            <option value="rejected">{{ t('shop.status.rejected') }}</option>
          </select>
        </div>
      </div>

      <div v-if="delivered" class="aalert success shop-delivered">
        <div class="aalert-body">
          <span class="aalert-title">{{ t('shop.orders.subLink') }}<span v-if="delivered.name"> — {{ delivered.name }}</span></span>
          <div class="acompact">
            <label class="ainput block"><input :value="delivered.link" readonly class="ltr" @focus="$event.target.select()" /></label>
            <button type="button" class="abtn" @click="copyLink"><Icon name="copy" :size="14" /><span>{{ t('action.copy') }}</span></button>
          </div>
        </div>
        <button type="button" class="aalert-close" :aria-label="t('action.cancel')" @click="delivered = null"><Icon name="close" :size="14" /></button>
      </div>

      <div class="tw">
        <table>
          <thead>
            <tr>
              <th>{{ t('shop.orders.buyer') }}</th>
              <th>{{ t('shop.orders.plan') }}</th>
              <th>{{ t('shop.orders.amount') }}</th>
              <th>{{ t('shop.orders.method') }}</th>
              <th>{{ t('shop.orders.status') }}</th>
              <th>{{ t('shop.orders.when') }}</th>
              <th class="w-md"></th>
            </tr>
          </thead>
          <tbody>
            <tr v-if="!orders.length" class="empty-row">
              <td colspan="7">
                <div class="card-empty"><div>{{ t('shop.orders.empty') }}</div></div>
              </td>
            </tr>
            <tr v-for="o in orders" :key="o.id">
              <td>
                <span>{{ o.buyerName || '—' }}</span>
                <div class="sub muted small ltr">{{ o.buyerTgId }}</div>
              </td>
              <td>
                <span>{{ o.planName }}</span>
                <span class="dtag" :class="SECTION_TONES[sectionOf(o)]">{{ t(`shop.section.${sectionOf(o)}`) }}</span>
              </td>
              <td class="num">{{ money(o.priceToman) }}</td>
              <td><span class="dtag gray">{{ t(`shop.orders.method.${o.method}`) }}</span></td>
              <td><span class="dtag" :class="STATUS_TONES[o.status] || 'gray'">{{ t(`shop.status.${o.status}`) }}</span></td>
              <td class="small muted">{{ dateTime(o.createdAt, store.locale) }}</td>
              <td>
                <div v-if="actionable(o) || (o.method === 'card' && o.hasReceipt)" class="row">
                  <button v-if="o.method === 'card' && o.hasReceipt" class="btn sm ghost" :disabled="orderBusy === o.id" @click="viewReceipt(o)">
                    <Icon name="eye" :size="14" /><span>{{ t('shop.orders.receipt') }}</span>
                  </button>
                  <template v-if="actionable(o)">
                    <button class="btn sm primary" :disabled="orderBusy === o.id" @click="approveOrder(o)">
                      <span v-if="orderBusy === o.id" class="spin sm"></span>
                      <span>{{ t('shop.orders.approve') }}</span>
                    </button>
                    <button class="btn sm danger-ghost" :disabled="orderBusy === o.id" @click="orderReject = o">{{ t('shop.orders.reject') }}</button>
                  </template>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>
    <!-- The people the bot turned into customers of this operator.
         Managing one is the customer page's work, so the row links
         there; speaking to them is the bot's, and the card below it. -->
    <div class="card">
      <div class="card-head">
        <h2>{{ t('shop.buyers.title') }}</h2>
      </div>
      <div class="tw">
        <table>
          <thead>
            <tr>
              <th>{{ t('shop.buyers.name') }}</th>
              <th>{{ t('shop.buyers.telegramId') }}</th>
              <th>{{ t('shop.orders.status') }}</th>
              <th>{{ t('shop.buyers.expires') }}</th>
              <th>{{ t('shop.buyers.usage') }}</th>
              <th>{{ t('shop.buyers.orders') }}</th>
              <th>{{ t('shop.buyers.wallet') }}</th>
              <th class="w-md"></th>
            </tr>
          </thead>
          <tbody>
            <tr v-if="!buyers.length" class="empty-row">
              <td colspan="8">
                <div class="card-empty"><div>{{ t('shop.buyers.empty') }}</div></div>
              </td>
            </tr>
            <tr v-for="b in buyers" :key="b.clientId">
              <td>{{ b.name || '—' }}</td>
              <td class="num ltr">{{ b.telegramId }}</td>
              <td><span class="dtag" :class="BUYER_TONES[b.status] || 'gray'">{{ t(`status.${b.status}`) }}</span></td>
              <td class="small muted">{{ b.expiresAt ? dateTime(b.expiresAt, store.locale) : t('shop.plans.unlimited') }}</td>
              <td class="num">{{ bytes(b.usedBytes, store.locale) }} / {{ b.quotaBytes ? bytes(b.quotaBytes, store.locale) : t('shop.plans.unlimited') }}</td>
              <td class="num">{{ nf(b.orders) }}</td>
              <td class="num">{{ money(b.walletBalance) }}</td>
              <td>
                <div class="row">
                  <button class="btn sm ghost" @click="openAnnounce(b)">
                    <Icon name="send" :size="14" /><span>{{ t('shop.buyers.announce') }}</span>
                  </button>
                  <RouterLink class="btn sm ghost" :to="`/clients/${b.clientId}`">{{ t('shop.buyers.manage') }}</RouterLink>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <!-- Speaking to the bot's customers, through the bot: one buyer, or
         everyone it has sold to. -->
    <div id="shop-announce" class="card">
      <div class="card-head">
        <h2>{{ t('shop.announce.title') }}</h2>
      </div>
      <div class="card-body shop-form">
        <div class="setting-list-item"><div class="arow">
          <div class="acol"><div class="setting-list-meta">
            <div class="setting-list-title">{{ t('shop.announce.audience') }}</div>
          </div></div>
          <div class="acol">
            <div class="aselect">
              <select v-model="announce.target" :aria-label="t('shop.announce.audience')">
                <option :value="0">{{ t('shop.announce.all') }}</option>
                <option v-for="b in buyers" :key="b.clientId" :value="b.telegramId">{{ b.name }} · {{ b.telegramId }}</option>
              </select>
            </div>
          </div>
        </div></div>

        <div class="field">
          <label for="shop-announce-text">{{ t('shop.announce.text') }}</label>
          <textarea id="shop-announce-text" v-model="announce.text" rows="4"></textarea>
        </div>

        <p v-if="!cfg?.botRunning" class="muted small">{{ t('shop.announce.botOff') }}</p>

        <div class="shop-save">
          <button class="btn primary" :disabled="announceBusy || !announceReady" @click="sendAnnounce">
            <span v-if="announceBusy" class="spin sm"></span>
            <Icon v-else name="send" :size="14" />
            <span>{{ t('shop.announce.send') }}</span>
          </button>
        </div>
      </div>
    </div>
  </div>

  <!-- One plan, made or mended. The quota is typed in gigabytes, as plans
       are sold, and turned into the bytes the panel counts in on save. -->
  <div v-if="planEdit" class="modal-backdrop" @click.self="planEdit = null">
    <div class="modal narrow" role="dialog" aria-modal="true">
      <div class="card-head">
        <h2>{{ planEdit.id ? t('shop.plans.edit') : t('shop.plans.new') }}</h2>
        <button class="btn sm icon ghost spacer" :aria-label="t('action.cancel')" @click="planEdit = null">
          <Icon name="close" :size="15" />
        </button>
      </div>
      <form id="shop-plan-form" class="card-body shop-form" @submit.prevent="savePlan">
        <div class="field">
          <label for="sp-name">{{ t('shop.plans.name') }}</label>
          <input id="sp-name" v-model="planEdit.name" type="text" maxlength="60" autofocus />
        </div>
        <div class="field">
          <label for="sp-section">{{ t('shop.plans.section') }}</label>
          <div class="aselect">
            <select id="sp-section" v-model="planEdit.section">
              <option value="wireguard">{{ t('shop.section.wireguard') }}</option>
              <option value="v2ray">{{ t('shop.section.v2ray') }}</option>
            </select>
          </div>
        </div>
        <div class="shop-grid">
          <div class="field">
            <label for="sp-quota">{{ t('shop.plans.quotaGb') }}</label>
            <input id="sp-quota" v-model="planEdit.quotaGb" type="number" min="0" step="any" inputmode="decimal" class="ltr" placeholder="0" />
            <span class="hint">{{ t('shop.plans.unlimited') }}: 0</span>
          </div>
          <div class="field">
            <label for="sp-days">{{ t('shop.plans.days') }}</label>
            <input id="sp-days" v-model="planEdit.days" type="number" min="1" step="1" inputmode="numeric" class="ltr" />
          </div>
          <div class="field">
            <label for="sp-price">{{ t('shop.plans.price') }}</label>
            <input id="sp-price" v-model="planEdit.priceToman" type="number" min="0" step="1" inputmode="numeric" class="ltr" />
          </div>
          <div class="field">
            <label for="sp-devices">{{ t('shop.plans.deviceLimit') }}</label>
            <input id="sp-devices" v-model="planEdit.deviceLimit" type="number" min="0" step="1" inputmode="numeric" class="ltr" placeholder="0" />
            <span class="hint">{{ t('shop.plans.unlimited') }}: 0</span>
          </div>
          <div class="field">
            <label for="sp-sort">{{ t('shop.plans.sort') }}</label>
            <input id="sp-sort" v-model="planEdit.sort" type="number" step="1" inputmode="numeric" class="ltr" placeholder="0" />
          </div>
          <div class="field">
            <label>{{ t('shop.plans.enabled') }}</label>
            <Toggle v-model="planEdit.enabled" :label="t('shop.plans.enabled')" />
          </div>
        </div>
      </form>
      <div class="modal-foot">
        <button type="button" class="btn ghost" @click="planEdit = null">{{ t('action.cancel') }}</button>
        <button type="submit" form="shop-plan-form" class="btn primary" :disabled="!planValid || planBusy">
          <span v-if="planBusy" class="spin"></span>
          <span v-else>{{ t('action.save') }}</span>
        </button>
      </div>
    </div>
  </div>

  <!-- One code, made or mended. Its uses are not in the form: they are
       the store's record of what was spent, and an edit that could
       rewrite them would hand spent uses back. -->
  <div v-if="codeEdit" class="modal-backdrop" @click.self="codeEdit = null">
    <div class="modal narrow" role="dialog" aria-modal="true">
      <div class="card-head">
        <h2>{{ codeEdit.id ? t('shop.codes.edit') : t('shop.codes.new') }}</h2>
        <button class="btn sm icon ghost spacer" :aria-label="t('action.cancel')" @click="codeEdit = null">
          <Icon name="close" :size="15" />
        </button>
      </div>
      <form id="shop-code-form" class="card-body shop-form" @submit.prevent="saveCode">
        <div class="field">
          <label for="sc-code">{{ t('shop.codes.code') }}</label>
          <input id="sc-code" v-model="codeEdit.code" type="text" maxlength="64" autocomplete="off" spellcheck="false" class="ltr" autofocus />
        </div>
        <div class="field">
          <label for="sc-kind">{{ t('shop.codes.kind') }}</label>
          <div class="aselect">
            <select id="sc-kind" v-model="codeEdit.kind">
              <option value="percent">{{ t('shop.codes.kind.percent') }}</option>
              <option value="fixed">{{ t('shop.codes.kind.fixed') }}</option>
              <option value="gift">{{ t('shop.codes.kind.gift') }}</option>
            </select>
          </div>
        </div>
        <div class="shop-grid">
          <div class="field">
            <label for="sc-value">{{ t('shop.codes.value') }}</label>
            <input id="sc-value" v-model="codeEdit.value" type="number" min="0" step="1" inputmode="numeric" class="ltr" />
            <span class="hint">{{ codeEdit.kind === 'percent' ? t('shop.codes.valuePercentHint') : t('shop.codes.valueMoneyHint') }}</span>
          </div>
          <div class="field">
            <label for="sc-max">{{ t('shop.codes.maxUses') }}</label>
            <input id="sc-max" v-model="codeEdit.maxUses" type="number" min="0" step="1" inputmode="numeric" class="ltr" placeholder="0" />
            <span class="hint">{{ t('shop.plans.unlimited') }}: 0</span>
          </div>
          <div class="field">
            <label for="sc-expires">{{ t('shop.codes.expires') }}</label>
            <input id="sc-expires" v-model="codeEdit.expiresDate" type="date" class="ltr" />
            <span class="hint">{{ t('shop.codes.noExpiry') }}: —</span>
          </div>
          <div class="field">
            <label>{{ t('shop.plans.enabled') }}</label>
            <Toggle v-model="codeEdit.enabled" :label="t('shop.plans.enabled')" />
          </div>
        </div>
      </form>
      <div class="modal-foot">
        <button type="button" class="btn ghost" @click="codeEdit = null">{{ t('action.cancel') }}</button>
        <button type="submit" form="shop-code-form" class="btn primary" :disabled="!codeValid || codeBusy">
          <span v-if="codeBusy" class="spin"></span>
          <span v-else>{{ t('action.save') }}</span>
        </button>
      </div>
    </div>
  </div>

  <!-- A card order's receipt, fetched with the session and read here:
       the seller looks at the photo and decides without leaving it,
       which is the whole errand this order is waiting on. -->
  <div v-if="receiptView" class="modal-backdrop" @click.self="closeReceipt">
    <div class="modal narrow" role="dialog" aria-modal="true">
      <div class="card-head">
        <h2>{{ t('shop.orders.receipt') }}</h2>
        <button class="btn sm icon ghost spacer" :aria-label="t('action.cancel')" @click="closeReceipt">
          <Icon name="close" :size="15" />
        </button>
      </div>
      <div class="card-body">
        <p class="muted small">
          {{ receiptView.order.buyerName || '—' }} · {{ receiptView.order.planName }} · {{ money(receiptView.order.priceToman) }}
        </p>
        <img class="shop-receipt-img" :src="receiptView.url" :alt="t('shop.orders.receipt')" />
      </div>
      <div v-if="receiptView.order.status === 'pending_review'" class="modal-foot">
        <button type="button" class="btn danger-ghost" :disabled="orderBusy === receiptView.order.id" @click="rejectReceipt">
          {{ t('shop.orders.reject') }}
        </button>
        <button type="button" class="btn primary" :disabled="orderBusy === receiptView.order.id" @click="approveReceipt">
          <span v-if="orderBusy === receiptView.order.id" class="spin"></span>
          <span v-else>{{ t('shop.orders.approve') }}</span>
        </button>
      </div>
    </div>
  </div>

  <ConfirmDialog
    :open="!!planDelete"
    :title="t('action.delete')"
    :body="t('shop.plans.deleteConfirm')"
    :subject="planDelete?.name || ''"
    :confirm-label="t('action.delete')"
    :danger="true"
    :busy="planBusy"
    @confirm="deletePlan"
    @cancel="planDelete = null"
  />

  <ConfirmDialog
    :open="!!codeDelete"
    :title="t('action.delete')"
    :body="t('shop.codes.deleteConfirm')"
    :subject="codeDelete?.code || ''"
    :confirm-label="t('action.delete')"
    :danger="true"
    :busy="codeBusy"
    @confirm="deleteCode"
    @cancel="codeDelete = null"
  />

  <ConfirmDialog
    :open="!!orderReject"
    :title="t('shop.orders.reject')"
    :body="t('shop.orders.rejectConfirm')"
    :subject="orderReject?.planName || ''"
    :confirm-label="t('shop.orders.reject')"
    :danger="true"
    :busy="orderBusy !== 0"
    @confirm="rejectOrder"
    @cancel="orderReject = null"
  />
</template>

<style scoped>
.shop-stack {
  display: flex;
  flex-direction: column;
  gap: 18px;
}
/* The counters as the design's stat cards, five across when they fit;
   a counter at zero reads quiet, as it did as a tile. */
.shop-stats {
  grid-template-columns: repeat(auto-fit, minmax(150px, 1fr));
}
.shop-stats .stat.quiet .val {
  color: var(--faint);
}
/* A second heading inside the one config card, set off from the bot's half
   by the rule the card's own head draws under itself. */
.shop-subhead {
  border-top: 1px solid var(--line-soft);
}
.shop-form {
  display: flex;
  flex-direction: column;
}
.shop-save {
  display: flex;
  justify-content: flex-end;
  margin-top: 16px;
}
.shop-grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 14px;
}
.shop-delivered {
  margin: 0 16px 12px;
}
/* The receipt photo, as large as the modal lets it be without pushing
   the decision off the screen. */
.shop-receipt-img {
  display: block;
  max-width: 100%;
  max-height: 55vh;
  margin: 10px auto 0;
  border-radius: 8px;
}
</style>
