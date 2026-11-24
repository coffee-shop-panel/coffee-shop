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
const showSkeleton = useDelayed(computed(() => loading.value && !cfg.value))

// The config as the server last stated it (whether the bot is actually
// running, its username, whether the public address may be edited) and the
// form the operator edits. The token is never in either: the server says
// only whether one is on file, and the form's token field starts empty --
// empty means "keep it", which is the only thing it can mean.
const cfg = ref(null)
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
})
const saving = ref(false)

const stats = ref(null)
const plans = ref([])
const orders = ref([])
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
  }
}

async function load() {
  loading.value = true
  try {
    // The config is the page; the rest are summaries around it, and each is
    // allowed to fail without taking the page down with it.
    const [c, p, s, ifaces] = await Promise.all([
      api.shopConfig(),
      api.shopPlans().catch(() => null),
      api.shopStats().catch(() => null),
      api.interfaces().catch(() => []),
    ])
    cfg.value = c
    fillForm(c)
    plans.value = p?.items || []
    stats.value = s
    interfaces.value = ifaces || []
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
  if (!cfg.value?.enabled) return { text: t('shop.bot.off'), tone: 'grey' }
  if (cfg.value.botRunning) return { text: t('shop.bot.connected'), tone: 'green' }
  return { text: t('shop.bot.connecting'), tone: 'orange' }
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

function openPlan(plan) {
  planEdit.value = plan
    ? {
        id: plan.id,
        name: plan.name || '',
        quotaGb: bytesToGigabytes(plan.quotaBytes),
        days: plan.days || '',
        priceToman: plan.priceToman ?? '',
        deviceLimit: plan.deviceLimit ?? '',
        enabled: !!plan.enabled,
        sort: plan.sort ?? '',
      }
    : { id: 0, name: '', quotaGb: '', days: '', priceToman: '', deviceLimit: '', enabled: true, sort: '' }
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

// ── orders ───────────────────────────────────────────────────────────────

const STATUS_TONES = {
  unpaid: 'grey',
  awaiting_receipt: 'grey',
  pending_review: 'orange',
  paid: 'blue',
  fulfilled: 'green',
  rejected: 'red',
  expired: 'grey',
  failed: 'red',
}
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
</script>

<template>
  <PageSpin v-if="showSkeleton" />
  <div v-else-if="loading" class="empty"></div>
  <ErrorState v-else-if="loadError" :error="loadError" @retry="load" />

  <div v-else class="shop-stack">
    <!-- The shop at a glance, once the counters have loaded: what sold
         today, what has sold altogether, and what is waiting on the
         seller -- the one number here that asks for something. -->
    <div v-if="statTiles.length" class="ov-customers">
      <div v-for="tile in statTiles" :key="tile.key" class="card ov-cust" :class="{ quiet: !tile.n, urgent: tile.urgent }">
        <span class="ov-cust-label">{{ tile.label }}</span>
        <strong class="ov-cust-value num">{{ tile.value }}</strong>
      </div>
    </div>

    <!-- The bot and how it is paid are one config with one save, so they
         are one card with two halves rather than two cards whose saves
         could disagree about what was sent. -->
    <form class="card" @submit.prevent="saveConfig">
      <div class="card-head">
        <h2>{{ t('shop.bot.title') }}</h2>
        <span class="tag num" :class="botStatus.tone">{{ botStatus.text }}</span>
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
      <div class="table-wrap">
        <table>
          <thead>
            <tr>
              <th>{{ t('shop.plans.name') }}</th>
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
              <td colspan="7">
                <div class="card-empty"><div>{{ t('shop.plans.empty') }}</div></div>
              </td>
            </tr>
            <tr v-for="p in plans" :key="p.id">
              <td>{{ p.name }}</td>
              <td class="num">{{ p.quotaBytes ? bytes(p.quotaBytes, store.locale) : t('shop.plans.unlimited') }}</td>
              <td class="num">{{ nf(p.days) }} {{ t('unit.days') }}</td>
              <td class="num">{{ money(p.priceToman) }}</td>
              <td class="num">{{ p.deviceLimit ? nf(p.deviceLimit) : t('shop.plans.unlimited') }}</td>
              <td><span class="tag" :class="p.enabled ? 'green' : 'grey'">{{ p.enabled ? t('shop.plans.enabled') : t('shop.bot.off') }}</span></td>
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

      <div class="table-wrap">
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
              <td>{{ o.planName }}</td>
              <td class="num">{{ money(o.priceToman) }}</td>
              <td><span class="tag">{{ t(`shop.orders.method.${o.method}`) }}</span></td>
              <td><span class="tag" :class="STATUS_TONES[o.status] || 'grey'">{{ t(`shop.status.${o.status}`) }}</span></td>
              <td class="small muted">{{ dateTime(o.createdAt, store.locale) }}</td>
              <td>
                <div v-if="actionable(o)" class="row">
                  <button class="btn sm primary" :disabled="orderBusy === o.id" @click="approveOrder(o)">
                    <span v-if="orderBusy === o.id" class="spin sm"></span>
                    <span>{{ t('shop.orders.approve') }}</span>
                  </button>
                  <button class="btn sm danger-ghost" :disabled="orderBusy === o.id" @click="orderReject = o">{{ t('shop.orders.reject') }}</button>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
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
/* The counters' grid carries a bottom margin of its own on the overview;
   here the stack's gap already does that work. */
.shop-stack .ov-customers {
  margin-bottom: 0;
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
</style>
