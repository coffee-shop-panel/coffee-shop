<script setup>
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { RouterLink } from 'vue-router'
import { api } from '../lib/api.js'
import { useDelayed } from '../lib/live.js'
import { store, t, notify } from '../lib/store.js'
import { bytes, relative } from '../lib/format.js'
import { transportTag } from '../lib/interfaces/rows.js'
import Icon from '../components/Icon.vue'
import AntIcon from '../components/AntIcon.vue'
import Sparkline from '../components/Sparkline.vue'
import ErrorState from '../components/ErrorState.vue'
import ConfirmDialog from '../components/ConfirmDialog.vue'
import PageSpin from '../components/PageSpin.vue'
import UpdateDialog from '../components/overview/UpdateDialog.vue'
import { useUpdate } from '../lib/update.js'
import OverviewVitals from '../components/overview/OverviewVitals.vue'
import LogsDialog from '../components/overview/LogsDialog.vue'
import BackupDialog from '../components/overview/BackupDialog.vue'
import HistoryDialog from '../components/overview/HistoryDialog.vue'

const data = ref(null)
// Which of the page's dialogs is open.
const logsOpen = ref(false)
const backupOpen = ref(false)
const historyOpen = ref(false)
const loading = ref(true)
let timer = null

// loadError is only shown when there is nothing to show instead. A refresh
// that fails while the page already holds figures should not replace them with
// an error: stale numbers with a warning are more use than none.
const loadError = ref(null)

async function load(quiet = false) {
  try {
    data.value = await api.fullOverview({ background: quiet })
    loadError.value = null
  } catch (err) {
    loadError.value = err
    if (!quiet) notify(err.message, 'error')
  } finally {
    loading.value = false
  }
}

// The recent-activity card reads the owner's activity log. It is the
// owner's alone — anyone else is refused, and the card simply never
// appears rather than showing an error on a page that is not theirs.
const activity = ref(null)
async function loadActivity() {
  try {
    const res = await api.activity(0, 6)
    activity.value = res?.items || []
  } catch {
    activity.value = []
  }
}

onMounted(async () => {
  await load()
  loadActivity()
  // The server samples on its own ticker and keeps the history; polling only
  // pulls the latest window. Failures stay quiet so a brief blip does not stack
  // toasts on an unattended dashboard.
  timer = setInterval(() => load(true), 3000)
})
onUnmounted(() => clearInterval(timer))

const sys = computed(() => data.value?.system)

// A first load only: the three-second refresh must never put a skeleton over a
// page that already has numbers on it.
//
// Declared after `sys`, not before it: useDelayed watches with `immediate`, so
// it reads the source during setup -- placed any earlier it reaches a `const`
// that does not exist yet and the whole page fails to render.
const showSkeleton = useDelayed(computed(() => loading.value && !sys.value))
const panel = computed(() => data.value?.panel)
const clients = computed(() => data.value?.clients)

// The greeting, by the clock on the machine reading it, and the day it is.
const greeting = computed(() => {
  const h = new Date().getHours()
  const key = h < 12 ? 'overview.greetingMorning' : h < 18 ? 'overview.greetingAfternoon' : 'overview.greetingEvening'
  const name = store.admin?.username
  return name ? `${t(key)}, ${name}` : t(key)
})
const dateLine = computed(
  () => new Intl.DateTimeFormat(store.locale, { weekday: 'long', day: 'numeric', month: 'long' }).format(new Date()),
)

// The tiles an operator acts on, in the order they matter. The ones that mean
// work sit first; the reassuring totals sit last.
const customerTiles = computed(() => {
  const c = clients.value
  if (!c) return []
  return [
    { key: 'online', label: t('overview.onlineNow'), value: c.online, tone: 'ok', icon: 'GlobalOutlined', foot: t('stat.footOnline'), to: '/clients?status=online' },
    { key: 'depleting', label: t('stat.depleting'), value: c.depleting, tone: 'warn', icon: 'WarningOutlined', foot: t('stat.footDepleting'), urgent: true, to: '/clients?status=depleting' },
    { key: 'exhausted', label: t('status.exhausted'), value: c.exhausted, tone: 'bad', icon: 'StopOutlined', foot: t('stat.footExhausted'), urgent: true, to: '/clients?status=exhausted' },
    { key: 'expired', label: t('status.expired'), value: c.expired, tone: 'bad', icon: 'CalendarOutlined', foot: t('stat.footExpired'), urgent: true, to: '/clients?status=expired' },
    { key: 'active', label: t('status.active'), value: c.active, tone: 'acc', icon: 'CheckCircleOutlined', foot: t('stat.footActive'), to: '/clients?status=active' },
    { key: 'total', label: t('nav.clients'), value: c.clients, tone: '', icon: 'TeamOutlined', foot: t('stat.footClients'), to: '/clients' },
  ]
})

// What needs doing today, as the design's card lists it: the counts that
// mean work, each with its share of the whole list and the way out.
const attention = computed(() => {
  const c = clients.value
  if (!c) return { rows: [], total: 0 }
  const share = (n) => (c.clients ? Math.min(100, Math.round(((n || 0) / c.clients) * 100)) : 0)
  const rows = [
    { key: 'depleting', count: c.depleting, icon: 'WarningOutlined', tone: 'warn', label: t('stat.depleting'), sub: t('overview.attnDepleting'), action: t('overview.addTraffic'), to: '/clients?status=depleting' },
    { key: 'exhausted', count: c.exhausted, icon: 'StopOutlined', tone: 'bad', label: t('status.exhausted'), sub: t('overview.attnExhausted'), action: t('overview.resetRenew'), to: '/clients?status=exhausted' },
    { key: 'expired', count: c.expired, icon: 'CalendarOutlined', tone: 'bad', label: t('status.expired'), sub: t('overview.attnExpired'), action: t('overview.addTime'), to: '/clients?status=expired' },
  ]
    .filter((r) => r.count > 0)
    .map((r) => ({ ...r, share: share(r.count) }))
  return { rows, total: rows.reduce((a, r) => a + r.count, 0) }
})

// The host's traffic, split the way it moved: the card's headline is the
// two directions together, the split meter their shares of it.
const totalUsage = computed(() => {
  const n = sys.value?.network
  if (!n) return null
  const sent = n.bytesSent || 0
  const recv = n.bytesRecv || 0
  const all = sent + recv
  const split = (v) => {
    const s = bytes(v, store.locale)
    const at = s.lastIndexOf(' ')
    return at > 0 ? { num: s.slice(0, at), unit: s.slice(at + 1) } : { num: s, unit: '' }
  }
  return {
    total: split(all),
    sentText: bytes(sent, store.locale),
    recvText: bytes(recv, store.locale),
    sentPct: all ? (sent / all) * 100 : 0,
    recvPct: all ? (recv / all) * 100 : 0,
  }
})

// The online card's line is the host's traffic, both directions summed:
// the shape of everything moving now, which is what "online" feels like.
const onlineSpark = computed(() => {
  const h = sys.value?.history || {}
  const up = h.up || []
  const down = h.down || []
  const n = Math.min(up.length, down.length)
  const out = []
  for (let i = 0; i < n; i++) out.push((up[i] || 0) + (down[i] || 0))
  return out
})

const ifaces = computed(() => data.value?.interfaces || [])

const nf = (n) => Number(n || 0).toLocaleString(store.locale)

// How much of this server is actually carrying traffic, which is the state an
// operator wants at a glance and the thing the two controls below act on.
const tunnelsUp = computed(() => ifaces.value.filter((i) => i.enabled && i.running).length)

// The interface list is reference, not this page's question, and a fleet's
// worth of rows buries everything below it — so the section starts folded
// and opens only when an operator asks. The choice is remembered, the way
// the Interfaces page remembers which of its tabs was left open.
const IFACES_OPEN_KEY = 'wui.overview.ifacesOpen'
const ifacesOpen = ref(readIfacesOpen())
function readIfacesOpen() {
  try { return localStorage.getItem(IFACES_OPEN_KEY) === '1' } catch { return false }
}
function toggleIfaces() {
  ifacesOpen.value = !ifacesOpen.value
  try { localStorage.setItem(IFACES_OPEN_KEY, ifacesOpen.value ? '1' : '0') } catch { /* private window */ }
}

const planeBusy = ref(false)
const confirmStop = ref(false)

async function tunnelAction(path, message) {
  planeBusy.value = true
  try {
    const res = await api.post(`/api/tunnels/${path}`)
    // A tunnel that would not come up is the whole content of the answer, so it
    // is said rather than folded into a count that looks like success.
    const failed = Object.entries(res?.failures || {})
    if (failed.length) {
      notify(failed.map(([name, why]) => `${name}: ${why}`).join('\n'), 'error')
    } else {
      notify(message.replace('{n}', res?.interfaces ?? 0), 'success')
    }
    await load(true)
  } catch (e) {
    notify(e.message, 'error')
  } finally {
    planeBusy.value = false
  }
}

const restartTunnels = () => tunnelAction('restart', t('overview.restartedAll'))
const startTunnels = () => tunnelAction('start', t('overview.startedAll'))

function stopTunnels() {
  confirmStop.value = false
  return tunnelAction('stop', t('overview.stoppedAll'))
}

// v2.3.0 however the build was stamped; a development build says so as is.
const versionLabel = computed(() => {
  const v = String(panel.value?.version || '')
  return /^v?[0-9]/.test(v) ? 'v' + v.replace(/^v/, '') : v
})
// The update: offered in the header, followed in its dialog.
const { update, updateOpen, installing, openUpdate } = useUpdate(computed(() => panel.value?.version))

// The design's protocol tile for a tunnel: its shape by protocol, its
// short name by what the peer software calls it.
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
  <!-- The shape of the page while it is still arriving, rather than a spinner
       where the page will be. Only once the load has run long enough to be
       worth admitting to: this reading refreshes every three seconds, and a
       skeleton that flashed on each of them would be unusable. -->
  <PageSpin v-if="showSkeleton" />

  <div v-else-if="loading" class="ov-page cs"></div>

  <ErrorState v-else-if="loadError && !sys" :error="loadError" @retry="load()" />

  <div v-else-if="sys" class="ov-page cs">
    <div class="ph">
      <div>
        <h2>{{ greeting }}</h2>
        <p>{{ dateLine }} · {{ t('overview.pageNote') }}</p>
      </div>
      <div class="actions">
        <RouterLink to="/clients" class="btn">{{ t('client.menu.bulk') }}</RouterLink>
        <RouterLink to="/clients" class="btn primary">{{ t('client.menu.addClients') }}</RouterLink>
      </div>
    </div>

    <!-- The state of the server and the things an operator reaches for
         while looking at this page, in one strip: what is running and
         which version, the newer release when there is one, and the
         controls that act on the whole page's subject. -->
    <div class="card mb2">
      <div class="cb tight row">
        <span class="pill" :class="tunnelsUp ? 'ok' : 'warn'" :title="t('overview.tunnelsHint')">
          <span class="d"></span>{{ t('overview.tunnels') }} · {{ tunnelsUp }} / {{ ifaces.length }}
        </span>
        <button type="button" class="pill ver" :title="t('update.check')" @click="openUpdate">{{ versionLabel }}</button>
        <!-- An install under way, or a newer release to install. Only an
             operator who can install it is told: the check is theirs alone. -->
        <button v-if="installing && installing.stage !== 'failed'" type="button" class="pill upd" @click="updateOpen = true">
          <span class="spin sm"></span>
          <span>{{ t('update.updating') }}</span>
        </button>
        <button v-else-if="update?.available && update?.latest" type="button" class="pill upd" @click="openUpdate">
          <AntIcon name="CloudUploadOutlined" />
          <span>{{ t('update.cta') }} <bdi class="ltr">v{{ update.latest }}</bdi></span>
        </button>
        <span class="pill" :class="panel?.enforcementActive ? 'ok' : 'warn'">
          <span class="d"></span>{{ panel?.enforcementActive ? t('enforcement.active') : t('enforcement.unavailable') }}
        </span>
        <div class="row tight spacer">
          <button class="btn sm" :title="t('overview.restartAllHint')"
                  :disabled="planeBusy || !ifaces.length" @click="restartTunnels">
            <Icon name="refresh" :size="14" /><span class="lbl">{{ t('overview.restartAll') }}</span>
          </button>
          <button v-if="tunnelsUp" class="btn sm danger" :title="t('overview.stopAllHint')"
                  :disabled="planeBusy" @click="confirmStop = true">
            <Icon name="power" :size="14" /><span class="lbl">{{ t('overview.stopAll') }}</span>
          </button>
          <button v-else-if="ifaces.length" class="btn sm" :title="t('overview.startAllHint')"
                  :disabled="planeBusy" @click="startTunnels">
            <Icon name="power" :size="14" /><span class="lbl">{{ t('overview.startAll') }}</span>
          </button>
          <button class="btn sm" :title="t('history.hint')" @click="historyOpen = true">
            <Icon name="clock" :size="14" /><span class="lbl">{{ t('history.title') }}</span>
          </button>
          <button class="btn sm" :title="t('overview.viewLogs')" @click="logsOpen = true">
            <Icon name="info" :size="14" /><span class="lbl">{{ t('settings.tab.logs') }}</span>
          </button>
          <button class="btn sm" :title="t('overview.backupRestore')" @click="backupOpen = true">
            <Icon name="database" :size="14" /><span class="lbl">{{ t('overview.backupRestore') }}</span>
          </button>
          <!-- The system report is reached from the page about this server. It
               was in the settings menu, which is for settings. -->
          <RouterLink to="/settings/system" class="btn sm" :title="t('settings.tab.system')">
            <Icon name="server" :size="14" /><span class="lbl">{{ t('settings.tab.system') }}</span>
          </RouterLink>
          <RouterLink to="/settings" class="btn sm" :title="t('nav.settings')">
            <Icon name="settings" :size="14" /><span class="lbl">{{ t('nav.settings') }}</span>
          </RouterLink>
        </div>
      </div>
    </div>

    <div v-if="panel && !panel.enforcementActive" class="ov-health">
      <Icon name="alert" :size="16" />
      <span>{{ panel.enforcementMessage }}</span>
    </div>

    <!-- The bento row: who is on now, what needs doing, and what the
         server has carried, side by side. -->
    <div v-if="clients" class="grid g12 mb">
      <section class="card s5" style="overflow: hidden">
        <div class="cb">
          <div class="row between">
            <span class="kicker">{{ t('overview.onlineNow') }}</span>
            <span class="ddot ok live"></span>
          </div>
          <div class="row nowrap" style="align-items: flex-end; gap: 18px; margin-top: 10px">
            <div class="hero-num">{{ nf(clients.online) }}</div>
            <div style="flex: 1; min-width: 0">
              <Sparkline :series="[{ data: onlineSpark, color: 'var(--accent)' }]" :height="56" />
            </div>
          </div>
          <div class="row mt">
            <span class="faint small spacer">{{ t('overview.onlineNote') }}</span>
          </div>
        </div>
      </section>

      <section class="card s4" :class="{ hot: attention.total }">
        <div class="ch">
          <h3>{{ t('overview.needsAttention') }}</h3>
          <span v-if="attention.total" class="dtag warn spacer">{{ nf(attention.total) }}</span>
        </div>
        <div class="cb tight">
          <ul v-if="attention.rows.length" class="list">
            <li v-for="r in attention.rows" :key="r.key">
              <span class="ico-tile" :class="r.tone"><AntIcon :name="r.icon" /></span>
              <div class="col" style="flex: 1">
                <b>{{ nf(r.count) }} {{ r.label }}</b>
                <span class="faint xs">{{ r.sub }}</span>
                <div class="dmeter"><i :class="r.tone" :style="{ width: r.share + '%' }"></i></div>
              </div>
              <RouterLink :to="r.to" class="btn xs">{{ r.action }}</RouterLink>
            </li>
          </ul>
          <p v-else class="muted small" style="padding: 6px 0">{{ t('overview.allClear') }}</p>
        </div>
      </section>

      <section v-if="totalUsage" class="card s3">
        <div class="cb stack">
          <span class="kicker">{{ t('overview.totalUsage') }}</span>
          <div class="stat" style="padding: 0">
            <div class="val">{{ totalUsage.total.num }} <small>{{ totalUsage.total.unit }}</small></div>
          </div>
          <div class="dmeter split fat">
            <i :style="{ width: totalUsage.sentPct + '%' }"></i>
            <i class="sky" :style="{ width: totalUsage.recvPct + '%' }"></i>
          </div>
          <div class="legend">
            <span><i></i>↑ {{ totalUsage.sentText }} {{ t('overview.sent') }}</span>
            <span><i class="sky"></i>↓ {{ totalUsage.recvText }} {{ t('overview.received') }}</span>
          </div>
          <hr class="hr" style="margin: 6px 0" />
          <dl class="kv">
            <dt>{{ t('overview.activeCustomers') }}</dt>
            <dd><b>{{ nf(clients.active) }}</b> / {{ nf(clients.clients) }}</dd>
            <dt>{{ t('client.devices') }}</dt>
            <dd>{{ nf(clients.devices) }}</dd>
          </dl>
        </div>
      </section>
    </div>

    <!-- Customers first. This is the page an operator lands on, and the
         question they arrive with is whether anything needs doing today - not
         how the machine's memory is. Each tile leads to the list already
         filtered, so noticing a problem and acting on it is one click. -->
    <div v-if="clients" class="grid g6 mb">
      <RouterLink
        v-for="tile in customerTiles"
        :key="tile.key"
        :to="tile.to"
        class="card stat"
        :class="{ hot: tile.urgent && tile.value, quiet: !tile.value }"
      >
        <div class="lbl"><span class="ico-tile" :class="tile.tone"><AntIcon :name="tile.icon" /></span>{{ tile.label }}</div>
        <div class="val">{{ nf(tile.value) }}</div>
        <div class="foot">{{ tile.foot }}</div>
      </RouterLink>
    </div>

    <!-- Four vitals, the throughput chart, and the side stack. -->
    <OverviewVitals :system="sys" :panel="panel" />

    <!-- What this panel manages, beside what the operators last did. -->
    <div class="grid g12">
      <section class="card calm" :class="activity && activity.length ? 's8' : 's12'">
        <div class="ch">
          <h3>{{ t('nav.interfaces') }}</h3>
          <span class="sub">{{ t('overview.ifaceSummary', { n: ifaces.length, active: clients?.active ?? 0, total: clients?.clients ?? 0 }) }}</span>
          <!-- What the folded section still says: how many of the tunnels
               are actually up, so shutting the list costs no information. -->
          <span v-if="ifaces.length" class="dtag" :class="tunnelsUp ? 'ok' : 'gray'"><span class="d"></span>{{ nf(tunnelsUp) }} / {{ nf(ifaces.length) }} {{ t('overview.running') }}</span>
          <button v-if="ifaces.length" type="button" class="btn sm spacer iface-toggle" :aria-expanded="ifacesOpen" @click="toggleIfaces">
            <Icon name="chevronDown" :size="14" :class="{ turned: !ifacesOpen }" />
            <span>{{ ifacesOpen ? t('overview.hideList') : t('overview.showList') }}</span>
          </button>
          <RouterLink to="/interfaces" class="btn sm" :class="{ spacer: !ifaces.length }">{{ t('overview.openPage') }}</RouterLink>
        </div>

        <div v-if="!ifaces.length" class="dempty">
          <p>{{ t('interface.noneYet') }}</p>
          <RouterLink to="/interfaces" class="btn primary sm">
            <Icon name="plus" :size="14" />{{ t('interface.create') }}
          </RouterLink>
        </div>

        <div v-else-if="ifacesOpen" class="tw">
          <table>
            <thead>
              <tr>
                <th>{{ t('interface.name') }}</th>
                <th>{{ t('interface.endpoint') }}</th>
                <th>{{ t('interface.port') }}</th>
                <th>{{ t('interface.mode') }}</th>
                <th>{{ t('interface.subnet') }}</th>
                <th>{{ t('interface.capacity') }}</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="i in ifaces" :key="i.id">
                <td>
                  <div class="row nowrap">
                    <span class="proto" :class="tileOf(i).cls">{{ tileOf(i).text }}</span>
                    <div class="col">
                      <b class="mono">{{ i.name }}</b>
                      <span class="faint xs">{{ t(`protocol.${i.protocol}`) }} · {{ transportTag(i) }}</span>
                    </div>
                  </div>
                </td>
                <td class="mono">{{ i.endpointHost }}</td>
                <td class="mono">{{ i.listenPort }}</td>
                <td>
                  <span v-if="i.mode === 'amnezia'" class="dtag purple">AmneziaWG</span>
                  <span v-else class="muted small">{{ t('interface.mode.standard') }}</span>
                </td>
                <td class="mono">{{ i.subnet }}</td>
                <td style="min-width: 180px">
                  <div class="usage">
                    <div class="top"><b>{{ nf(i.allocated) }}</b><span>/ {{ nf(i.capacity) }}</span></div>
                    <div class="dmeter">
                      <i :class="i.capacity && i.allocated / i.capacity > 0.9 ? 'bad' : 'ok'"
                         :style="{ width: Math.max((i.allocated / (i.capacity || 1)) * 100, 1) + '%' }"></i>
                    </div>
                  </div>
                </td>
                <td>
                  <span v-if="i.enabled && i.running" class="dtag ok"><span class="d"></span>{{ t('overview.running') }}</span>
                  <span v-else-if="!i.enabled" class="dtag gray">{{ t('status.disabled') }}</span>
                  <span v-else class="dtag bad">{{ t('iface.stopped') }}</span>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>

      <section v-if="activity && activity.length" class="card s4">
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
  </div>

  <!-- Stopping every tunnel takes every customer offline at once, so it is
       asked for. Restarting is not: it changes no records and the worst case is
       a few seconds of reconnecting. -->
  <ConfirmDialog
    :open="confirmStop"
    :title="t('overview.stopAllTitle')"
    :body="t('overview.stopAllBody')"
    :confirm-label="t('overview.stopAll')"
    :danger="true"
    :busy="planeBusy"
    @confirm="stopTunnels"
    @cancel="confirmStop = false"
  />

  <UpdateDialog v-if="updateOpen" :current="panel?.version || ''" />
  <LogsDialog v-if="logsOpen" @close="logsOpen = false" />
  <BackupDialog v-if="backupOpen" @close="backupOpen = false" />
  <HistoryDialog v-if="historyOpen" @close="historyOpen = false" />
</template>

<style scoped>
.ov-page {
  --ov-gap: 12px;
  display: flex;
  flex-direction: column;
  gap: var(--ov-gap);
}
.ov-health {
  display: flex;
  align-items: flex-start;
  gap: 10px;
  padding: 11px 15px;
  border-radius: var(--radius-sm);
  background: var(--warn-soft);
  border: 1px solid rgba(224, 171, 52, 0.4);
  font-size: var(--t-sm);
  color: var(--ink);
}
.ov-health svg {
  color: var(--warn);
  flex-shrink: 0;
  margin-top: 2px;
}
/* The interfaces fold: the chevron rests pointing down while the list is
   open and turns aside when it is shut, the way the API page's groups do. */
.iface-toggle svg {
  transition: transform 0.15s;
}
.iface-toggle svg.turned {
  transform: rotate(-90deg);
}
[dir='rtl'] .iface-toggle svg.turned {
  transform: rotate(90deg);
}
</style>
