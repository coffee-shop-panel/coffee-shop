<script setup>
// The dashboard an operator who is not the owner lands on: their own
// corner of the panel. Everything on it is theirs -- their customers'
// figures, the tunnels they were given to sell with whether each is up,
// the customers about to run out, and their own latest activity --
// because the endpoint behind it counts nothing else. The owner's
// overview is a different page about the machine; this one is about
// the business the operator is running on it.
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { RouterLink } from 'vue-router'
import { api } from '../lib/api.js'
import { useDelayed } from '../lib/live.js'
import { store, t, notify } from '../lib/store.js'
import { bytes, relative, date } from '../lib/format.js'
import Icon from '../components/Icon.vue'
import AntIcon from '../components/AntIcon.vue'
import ErrorState from '../components/ErrorState.vue'
import PageSpin from '../components/PageSpin.vue'

const data = ref(null)
const loading = ref(true)
// Only fatal when there is nothing to show instead: a refresh that fails
// over figures already on the page leaves them standing, with a warning.
const loadError = ref(null)
let timer = null

async function load(quiet = false) {
  try {
    data.value = await api.overviewMine({ background: quiet })
    loadError.value = null
  } catch (err) {
    loadError.value = err
    if (!quiet) notify(err.message, 'error')
  } finally {
    loading.value = false
  }
}

onMounted(() => {
  load()
  // The tunnels' state is the part of this page that changes under the
  // operator, so it is kept fresh -- quietly, and slower than the owner's
  // telemetry, which is watching a machine rather than a book of customers.
  timer = setInterval(() => load(true), 30000)
})
onUnmounted(() => clearInterval(timer))

// Declared after the data refs it reads: useDelayed watches with
// immediate, so it reaches its source during setup.
const showSkeleton = useDelayed(computed(() => loading.value && !data.value))

const nf = (n) => Number(n || 0).toLocaleString(store.locale)

const greeting = computed(() => {
  const h = new Date().getHours()
  const key = h < 12 ? 'overview.greetingMorning' : h < 18 ? 'overview.greetingAfternoon' : 'overview.greetingEvening'
  const name = store.admin?.username
  return name ? `${t(key)}, ${name}` : t(key)
})
const dateLine = computed(
  () => new Intl.DateTimeFormat(store.locale, { weekday: 'long', day: 'numeric', month: 'long' }).format(new Date()),
)

const tiles = computed(() => {
  const d = data.value
  if (!d) return []
  return [
    { key: 'total', label: t('home.myCustomers'), value: d.clients, tone: 'acc', icon: 'TeamOutlined', foot: t('stat.footClients'), to: '/clients' },
    { key: 'active', label: t('status.active'), value: d.active, tone: 'ok', icon: 'CheckCircleOutlined', foot: t('stat.footActive'), to: '/clients?status=active' },
    { key: 'expired', label: t('status.expired'), value: d.expired, tone: 'bad', icon: 'CalendarOutlined', foot: t('stat.footExpired'), urgent: true, to: '/clients?status=expired' },
    { key: 'expiring', label: t('filter.bucket.expiring'), value: d.expiringSoon, tone: 'warn', icon: 'WarningOutlined', foot: t('home.footExpiring'), urgent: true, to: '/clients?status=expiring' },
  ]
})

// The ceiling, in the units it is enforced in: users sold against the
// limit. No limit is a fact to state, not a meter to draw.
const ceiling = computed(() => {
  const d = data.value
  if (!d) return null
  const limit = d.ceilingLimit || 0
  return {
    used: d.ceilingUsed || 0,
    limit,
    pct: limit ? Math.min(100, Math.round(((d.ceilingUsed || 0) / limit) * 100)) : 0,
    foot: limit ? t('home.ceilingOf', { limit: nf(limit) }) : t('admins.unlimited'),
  }
})

// This month's traffic, split the way the overview's usage card splits
// it: the number large, the unit small beside it.
const monthTraffic = computed(() => {
  const d = data.value
  if (!d) return null
  const s = bytes(d.monthUsedBytes || 0, store.locale)
  const at = s.lastIndexOf(' ')
  return at > 0 ? { num: s.slice(0, at), unit: s.slice(at + 1) } : { num: s, unit: '' }
})

const tunnels = computed(() => data.value?.tunnels || [])
const tunnelsUp = computed(() => tunnels.value.filter((x) => x.enabled && x.up).length)
const expiring = computed(() => data.value?.expiring || [])
const activity = computed(() => data.value?.activity || [])

// The design's protocol tile for a tunnel, as the overview draws it.
function tileOf(i) {
  if (i.protocol === 'wireguard') return i.mode === 'amnezia' ? { cls: 'awg', text: 'AWG' } : { cls: 'wg', text: 'WG' }
  if (i.protocol === 'openvpn') return { cls: 'ovpn', text: 'OVPN' }
  if (i.protocol === 'l2tp') return { cls: 'l2tp long', text: 'L2TP' }
  if (i.protocol === 'ikev2') return { cls: 'ikev2 long', text: 'IKEv2' }
  if (i.protocol === 'pptp') return { cls: 'pptp long', text: 'PPTP' }
  if (i.protocol === 'openconnect') return { cls: 'oc', text: 'OC' }
  return { cls: 'x', text: 'X' }
}

function activityTone(a) {
  if (a.status >= 200 && a.status < 300) return 'ok'
  if (a.status >= 400 && a.status < 500) return 'warn'
  if (a.status >= 500) return 'bad'
  return 'info'
}
</script>

<template>
  <PageSpin v-if="showSkeleton" />

  <div v-else-if="loading" class="ov-page cs"></div>

  <ErrorState v-else-if="loadError && !data" :error="loadError" @retry="load()" />

  <div v-else-if="data" class="ov-page cs">
    <div class="ph">
      <div>
        <h2>{{ greeting }}</h2>
        <p>{{ dateLine }} · {{ t('home.lede') }}</p>
      </div>
      <div class="actions">
        <RouterLink to="/report" class="btn">{{ t('nav.report') }}</RouterLink>
        <RouterLink to="/clients" class="btn primary">{{ t('client.menu.addClients') }}</RouterLink>
      </div>
    </div>

    <!-- The figures an operator arrives asking about, each leading to the
         customer list already narrowed to it. -->
    <div class="grid g6 mb">
      <RouterLink
        v-for="tile in tiles"
        :key="tile.key"
        :to="tile.to"
        class="card stat"
        :class="{ hot: tile.urgent && tile.value, quiet: !tile.value }"
      >
        <div class="lbl"><span class="ico-tile" :class="tile.tone"><AntIcon :name="tile.icon" /></span>{{ tile.label }}</div>
        <div class="val">{{ nf(tile.value) }}</div>
        <div class="foot">{{ tile.foot }}</div>
      </RouterLink>

      <div v-if="ceiling" class="card stat" :class="{ hot: ceiling.limit && ceiling.used >= ceiling.limit }">
        <div class="lbl"><span class="ico-tile"><AntIcon name="SafetyOutlined" /></span>{{ t('home.ceiling') }}</div>
        <div class="val">{{ nf(ceiling.used) }}<small v-if="ceiling.limit" class="ceil-of"> / {{ nf(ceiling.limit) }}</small></div>
        <div v-if="ceiling.limit" class="dmeter" style="margin-top: 8px">
          <i :class="ceiling.pct >= 100 ? 'bad' : ceiling.pct >= 90 ? 'warn' : 'ok'" :style="{ width: Math.max(ceiling.pct, 1) + '%' }"></i>
        </div>
        <div class="foot">{{ ceiling.foot }}</div>
      </div>

      <div v-if="monthTraffic" class="card stat">
        <div class="lbl"><span class="ico-tile sky"><AntIcon name="GlobalOutlined" /></span>{{ t('home.monthTraffic') }}</div>
        <div class="val">{{ monthTraffic.num }} <small class="ceil-of">{{ monthTraffic.unit }}</small></div>
        <div class="foot">{{ t('home.footMonth') }}</div>
      </div>
    </div>

    <div class="grid g12">
      <!-- The tunnels they were given, and whether each is up: the answer
           to the customer's "it doesn't connect" that a reseller could
           not check anywhere before. -->
      <section class="card s7">
        <div class="ch">
          <h3>{{ t('home.myTunnels') }}</h3>
          <span v-if="tunnels.length" class="dtag spacer" :class="tunnelsUp ? 'ok' : 'gray'">
            <span class="d"></span>{{ nf(tunnelsUp) }} / {{ nf(tunnels.length) }} {{ t('overview.running') }}
          </span>
        </div>
        <div v-if="!tunnels.length" class="dempty">
          <p>{{ t('home.noTunnels') }}</p>
        </div>
        <div v-else class="cb tight">
          <ul class="list">
            <li v-for="i in tunnels" :key="i.id">
              <span class="proto" :class="tileOf(i).cls">{{ tileOf(i).text }}</span>
              <div class="col" style="flex: 1">
                <b class="mono">{{ i.name }}</b>
                <span class="faint xs">{{ t(`protocol.${i.protocol}`) }}</span>
              </div>
              <span v-if="i.enabled && i.up" class="dtag ok"><span class="d"></span>{{ t('overview.running') }}</span>
              <span v-else-if="!i.enabled" class="dtag gray">{{ t('status.disabled') }}</span>
              <span v-else class="dtag bad">{{ t('iface.stopped') }}</span>
            </li>
          </ul>
        </div>
      </section>

      <!-- Who runs out first: the renewals to offer today, soonest first,
           each opening the customer it names. -->
      <section class="card s5">
        <div class="ch">
          <h3>{{ t('home.expiringTitle') }}</h3>
          <span v-if="data.expiringSoon" class="dtag warn spacer">{{ nf(data.expiringSoon) }}</span>
        </div>
        <div class="cb tight">
          <ul v-if="expiring.length" class="list">
            <li v-for="c in expiring" :key="c.id">
              <span class="ico-tile warn"><AntIcon name="CalendarOutlined" /></span>
              <div class="col" style="flex: 1">
                <RouterLink :to="`/clients/${c.id}`"><b>{{ c.name }}</b></RouterLink>
                <span class="faint xs">{{ date(c.expiresAt, store.locale) }}</span>
              </div>
              <span class="when">{{ relative(c.expiresAt, store.locale) }}</span>
            </li>
          </ul>
          <p v-else class="muted small" style="padding: 6px 0">{{ t('home.noneExpiring') }}</p>
        </div>
      </section>
    </div>

    <!-- Their own latest lines from the record, as the overview shows the
         owner's: what they last did, and what was refused. -->
    <section v-if="activity.length" class="card">
      <div class="ch">
        <h3>{{ t('overview.recentActivity') }}</h3>
        <RouterLink to="/activity" class="btn xs spacer">{{ t('nav.activity') }}</RouterLink>
      </div>
      <div class="cb tight">
        <ul class="list feed">
          <li v-for="a in activity" :key="a.id">
            <span class="ico-tile" :class="activityTone(a)">
              <Icon :name="activityTone(a) === 'ok' ? 'check' : activityTone(a) === 'info' ? 'info' : 'alert'" :size="15" />
            </span>
            <div class="col">
              <span><b>{{ a.actor }}</b> <span class="mono xs">{{ a.action }}</span></span>
              <span class="faint xs mono">{{ a.ip || '' }}<template v-if="a.reason"> · {{ a.reason }}</template></span>
            </div>
            <span class="when">{{ relative(a.at, store.locale) }}</span>
          </li>
        </ul>
      </div>
    </section>
  </div>
</template>

<style scoped>
.ov-page {
  --ov-gap: 12px;
  display: flex;
  flex-direction: column;
  gap: var(--ov-gap);
}
.ceil-of {
  font-size: 0.45em;
  font-weight: 500;
  color: var(--muted);
}
</style>
