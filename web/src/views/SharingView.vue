<script setup>
import { computed, onMounted, onBeforeUnmount, ref, watch } from 'vue'
import { RouterLink, useRouter } from 'vue-router'
import { api } from '../lib/api.js'
import { dateTime } from '../lib/format.js'
import { useDelayed } from '../lib/live.js'
import { store, t, tn, notify } from '../lib/store.js'
import AntIcon from '../components/AntIcon.vue'
import Icon from '../components/Icon.vue'
import ErrorState from '../components/ErrorState.vue'
import PageSpin from '../components/PageSpin.vue'

// Credentials seen from several places at once. Ours, not the classic panel's, so it
// is laid out the way its list pages are: a summary card of three figures,
// then a card whose title holds the actions and the search, and a small
// table with sortable columns and a menu column at the front.
//
// Two tabs: what is true now, and the record -- every device that has been
// on the list or held off for going over its plan, kept after it leaves the
// list and after the customer is deleted.

const TAB_KEY = 'wui.sharing.tab'
const router = useRouter()
const tab = ref(readTab())
const reports = ref([])
const events = ref([])
const importState = ref(null)
const loading = ref(true)
const loadError = ref(null)
const importing = ref(false)
const rows = computed(() => (tab.value === 'history' ? events.value : reports.value))
const showWait = useDelayed(computed(() => loading.value && !rows.value.length))
let timer = null

const nf = (n) => Number(n || 0).toLocaleString(store.locale)

function readTab() {
  try {
    return localStorage.getItem(TAB_KEY) === 'history' ? 'history' : 'now'
  } catch {
    return 'now'
  }
}
watch(tab, (v) => {
  try {
    localStorage.setItem(TAB_KEY, v)
  } catch {
    /* private window */
  }
  search.value = ''
  sort.value = { key: '', dir: '' }
  loading.value = true
  load()
})

onMounted(() => {
  load()
  // The window this looks at is ten minutes wide, so a slow refresh is enough
  // and a fast one would only add load for no new information.
  timer = setInterval(() => load(true), 30_000)
})
onBeforeUnmount(() => clearInterval(timer))

async function load(quiet = false) {
  try {
    if (tab.value === 'history') {
      const h = await api.get('/api/sharing/history', { background: quiet })
      events.value = h.events || []
      importState.value = h.import || null
    } else {
      reports.value = await api.get('/api/sharing', { background: quiet })
    }
    loadError.value = null
  } catch (e) {
    // As on the nodes page: a background poll failing must not replace the
    // rows on screen with an error. Only a first load says so.
    if (!quiet) loadError.value = e
    if (!quiet) notify(e.message, 'error')
  } finally {
    loading.value = false
  }
}

async function readLogAgain() {
  importing.value = true
  try {
    importState.value = await api.post('/api/sharing/history/import')
    await load(true)
  } catch (e) {
    notify(e.message, 'error')
  } finally {
    importing.value = false
  }
}

function since(iso) {
  if (!iso) return '—'
  const mins = Math.round((Date.now() - new Date(iso)) / 60000)
  if (mins < 60) return tn('sharing.minutesAgo', mins)
  const hours = Math.round(mins / 60)
  if (hours < 24) return tn('sharing.hoursAgo', hours)
  return tn('sharing.daysAgo', Math.round(hours / 24))
}

const addrsOf = (r) => (Array.isArray(r.addrs) ? r.addrs : String(r.addrs || '').split(',').filter(Boolean))

const totals = computed(() =>
  tab.value === 'history'
    ? {
        records: events.value.length,
        clients: new Set(events.value.map((r) => r.clientName)).size,
        held: events.value.filter((r) => r.kind === 'held').length,
      }
    : {
        devices: reports.value.length,
        clients: new Set(reports.value.map((r) => r.clientId)).size,
        addrs: reports.value.reduce((a, r) => a + addrsOf(r).length, 0),
      },
)

// What the record says about the time before it was kept.
const importNote = computed(() => {
  const s = importState.value
  if (!s) return null
  const n = (s.held || 0) + (s.shared || 0)
  switch (s.state) {
    case 'done':
      return { tone: 'info', text: n ? t('sharing.import.done', { n }) : t('sharing.import.doneNone'), retry: false }
    case 'denied':
      return { tone: 'warning', text: t('sharing.import.denied', { error: s.error || '' }), retry: true }
    case 'unavailable':
      return { tone: 'info', text: t('sharing.import.unavailable'), retry: false }
    default:
      return { tone: 'warning', text: t('sharing.import.failed', { error: s.error || '' }), retry: true }
  }
})

// Their search box beside the buttons: customer, device, address.
const search = ref('')
const visible = computed(() => {
  const q = search.value.trim().toLowerCase()
  if (!q) return rows.value
  return rows.value.filter((r) =>
    [r.clientName, r.deviceName, ...addrsOf(r)].some((v) => String(v || '').toLowerCase().includes(q)),
  )
})

// Their sortable headers: ascending, descending, then the server's order.
const sort = ref({ key: '', dir: '' })
const cmpText = (a, b) => String(a || '').localeCompare(String(b || ''), undefined, { numeric: true, sensitivity: 'base' })
const sorters = {
  client: (a, b) => cmpText(a.clientName, b.clientName),
  device: (a, b) => cmpText(a.deviceName, b.deviceName),
  addrs: (a, b) => addrsOf(a).length - addrsOf(b).length,
  since: (a, b) => new Date(a.since || 0) - new Date(b.since || 0),
  kind: (a, b) => cmpText(a.kind, b.kind),
  when: (a, b) => new Date(a.lastSeen || 0) - new Date(b.lastSeen || 0),
}
function sortCls(key) {
  return sort.value.key === key ? `sorted ${sort.value.dir}` : ''
}
function sortBy(key) {
  const cur = sort.value
  if (cur.key !== key) sort.value = { key, dir: 'asc' }
  else if (cur.dir === 'asc') sort.value = { key, dir: 'desc' }
  else sort.value = { key: '', dir: '' }
}
const sorted = computed(() => {
  const { key, dir } = sort.value
  if (!key || !sorters[key]) return visible.value
  const list = [...visible.value].sort(sorters[key])
  return dir === 'desc' ? list.reverse() : list
})

function open(r) {
  router.push(`/clients/${r.clientId}`)
}

// What a hold says: the plan's connections, and which server held it.
function detail(r) {
  const parts = []
  if (r.limit) parts.push(t('sharing.detail.limit', { n: r.limit }))
  if (r.place === 'here') parts.push(t('sharing.detail.here'))
  else if (r.place?.startsWith('node ')) parts.push(t('sharing.detail.node', { n: Number(r.place.slice(5)) || r.place.slice(5) }))
  return parts.length ? parts.join(store.locale === 'fa' ? '، ' : ', ') : '—'
}

// From and to, on one line when they are the same moment.
function when(r) {
  const from = dateTime(r.startedAt, store.locale)
  const to = dateTime(r.lastSeen, store.locale)
  return from === to ? from : `${from} – ${to}`
}
</script>

<template>
  <div class="cs">
    <div class="ph">
      <div>
        <h2>{{ t('nav.sharing') }}</h2>
        <p>{{ t('sharing.lede') }}</p>
      </div>
    </div>

    <!-- The summary, three figures, over whichever tab is showing. -->
    <div class="grid g3 mb2">
      <template v-if="tab === 'history'">
        <div class="card stat">
          <div class="lbl"><span class="ico-tile"><Icon name="clock" :size="14" /></span>{{ t('sharing.stat.records') }}</div>
          <div class="val">{{ nf(totals.records) }}</div>
        </div>
        <div class="card stat">
          <div class="lbl"><span class="ico-tile"><Icon name="users" :size="14" /></span>{{ t('sharing.stat.clients') }}</div>
          <div class="val">{{ nf(totals.clients) }}</div>
        </div>
        <div class="card stat" :class="{ hot: totals.held > 0 }">
          <div class="lbl"><span class="ico-tile warn"><Icon name="pause" :size="14" /></span>{{ t('sharing.stat.held') }}</div>
          <div class="val">{{ nf(totals.held) }}</div>
        </div>
      </template>
      <template v-else>
        <div class="card stat" :class="{ hot: totals.devices > 0 }">
          <div class="lbl"><span class="ico-tile warn"><Icon name="eye" :size="14" /></span>{{ t('sharing.stat.devices') }}</div>
          <div class="val">{{ nf(totals.devices) }}</div>
        </div>
        <div class="card stat">
          <div class="lbl"><span class="ico-tile"><Icon name="users" :size="14" /></span>{{ t('sharing.stat.clients') }}</div>
          <div class="val">{{ nf(totals.clients) }}</div>
        </div>
        <div class="card stat">
          <div class="lbl"><span class="ico-tile"><Icon name="globe" :size="14" /></span>{{ t('sharing.stat.addrs') }}</div>
          <div class="val">{{ nf(totals.addrs) }}</div>
          <div class="foot">{{ t('sharing.stat.window') }}</div>
        </div>
      </template>
    </div>

    <div class="tabs" role="tablist">
      <button class="tab" role="tab" :aria-selected="tab === 'now'" :class="{ on: tab === 'now' }" @click="tab = 'now'"><Icon name="eye" :size="14" /><span>{{ t('sharing.tab.now') }}</span></button>
      <button class="tab" role="tab" :aria-selected="tab === 'history'" :class="{ on: tab === 'history' }" @click="tab = 'history'"><Icon name="clock" :size="14" /><span>{{ t('sharing.tab.history') }}</span></button>
    </div>

    <!-- Said plainly and up front, because acting on this without reading
         it means disconnecting paying customers who did nothing wrong. -->
    <div v-if="tab === 'now'" class="banner info" role="note">
      <Icon name="info" :size="16" />
      <div><b>{{ t('sharing.readFirst') }}</b> {{ t('sharing.caveat') }}</div>
    </div>
    <template v-else>
      <div class="banner info" role="note">
        <Icon name="info" :size="16" />
        <div>{{ t('sharing.historyHint') }}</div>
      </div>
      <div v-if="importNote" class="banner" :class="importNote.tone === 'warning' ? 'warn' : 'info'" role="status">
        <Icon name="alert" :size="16" />
        <div>{{ importNote.text }}</div>
        <button v-if="importNote.retry" class="btn sm spacer" :disabled="importing" @click="readLogAgain">
          <span v-if="importing" class="spin sm"></span>
          <span>{{ importing ? t('sharing.import.running') : t('sharing.import.retry') }}</span>
        </button>
      </div>
    </template>

    <section class="card calm">
      <div class="toolbar">
        <button class="btn primary" @click="load()">
          <Icon name="refresh" :size="14" /><span>{{ t('common.refresh') }}</span>
        </button>
        <label class="ainput search-in">
          <span class="ainput-prefix"><AntIcon name="SearchOutlined" /></span>
          <input v-model="search" type="text" :placeholder="t('iface.menu.search')" :aria-label="t('iface.menu.search')" />
          <button v-if="search" type="button" class="ainput-clear" :aria-label="t('action.cancel')" @click="search = ''"><AntIcon name="CloseCircleFilled" /></button>
        </label>
      </div>

      <ErrorState v-if="loadError && !rows.length" :error="loadError" @retry="load()" />
      <PageSpin v-else-if="showWait" />
      <div v-else-if="loading" class="empty"></div>

      <!-- Now: what is on the list at this moment. -->
      <div v-else-if="tab === 'now'" class="tw">
        <table style="min-width: 760px">
          <thead>
            <tr>
              <th class="w0">{{ t('iface.col.menu') }}</th>
              <th class="sortable" :class="sortCls('client')" @click="sortBy('client')">{{ t('sharing.client') }}</th>
              <th class="sortable" :class="sortCls('device')" @click="sortBy('device')">{{ t('sharing.device') }}</th>
              <th class="sortable" :class="sortCls('addrs')" @click="sortBy('addrs')">{{ t('sharing.addresses') }}</th>
              <th class="sortable" :class="sortCls('since')" @click="sortBy('since')">{{ t('sharing.firstSeen') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-if="!sorted.length">
              <td colspan="5">
                <div class="dempty">
                  <span class="ico-tile ok"><Icon name="check" :size="22" /></span>
                  <div>{{ t('sharing.none') }}</div>
                  <div class="faint small">{{ t('sharing.noneHint') }}</div>
                </div>
              </td>
            </tr>
            <tr v-for="r in sorted" :key="r.accountId">
              <td class="w0">
                <button class="act" :title="t('sharing.review')" :aria-label="t('sharing.review')" @click="open(r)"><Icon name="eye" :size="16" /></button>
              </td>
              <td><RouterLink :to="`/clients/${r.clientId}`" class="name">{{ r.clientName }}</RouterLink></td>
              <td class="mono">{{ r.deviceName }}</td>
              <td>
                <div class="row tight">
                  <span v-for="a in addrsOf(r)" :key="a" class="dtag sq" dir="ltr">{{ a }}</span>
                </div>
              </td>
              <td>{{ since(r.since) }}</td>
            </tr>
          </tbody>
        </table>
      </div>

      <!-- History: every time, kept. -->
      <div v-else class="tw">
        <table class="history-table">
          <thead>
            <tr>
              <th class="sortable" :class="sortCls('kind')" @click="sortBy('kind')">{{ t('sharing.col.kind') }}</th>
              <th class="sortable" :class="sortCls('client')" @click="sortBy('client')">{{ t('sharing.client') }}</th>
              <th class="sortable" :class="sortCls('device')" @click="sortBy('device')">{{ t('sharing.device') }}</th>
              <th class="sortable" :class="sortCls('addrs')" @click="sortBy('addrs')">{{ t('sharing.addresses') }}</th>
              <th>{{ t('sharing.col.detail') }}</th>
              <th class="sortable" :class="sortCls('when')" @click="sortBy('when')">{{ t('sharing.col.when') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-if="!sorted.length">
              <td colspan="6">
                <div class="dempty">
                  <span class="ico-tile ok"><Icon name="check" :size="22" /></span>
                  <div>{{ t('sharing.historyNone') }}</div>
                </div>
              </td>
            </tr>
            <tr v-for="r in sorted" :key="r.id">
              <td>
                <span class="dtag" :class="r.kind === 'held' ? 'bad' : 'warn'">{{ t(`sharing.kind.${r.kind}`) }}</span>
                <span v-if="r.source === 'log'" class="dtag gray">{{ t('sharing.fromLog') }}</span>
              </td>
              <td>
                <RouterLink v-if="!r.deleted" :to="`/clients/${r.clientId}`" class="name">{{ r.clientName }}</RouterLink>
                <template v-else><span>{{ r.clientName }}</span> <span class="dtag gray">{{ t('sharing.deleted') }}</span></template>
              </td>
              <td class="mono">{{ r.deviceName }}</td>
              <td>
                <div class="row tight">
                  <span v-for="a in addrsOf(r)" :key="a" class="dtag sq" dir="ltr">{{ a }}</span>
                  <span v-if="!addrsOf(r).length" class="muted">—</span>
                </div>
              </td>
              <td class="muted">{{ detail(r) }}</td>
              <td class="when">{{ when(r) }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </section>

    <!-- What to do with a flag, in the order the design gives it: the
         four calm next steps under the Now list. -->
    <div v-if="tab === 'now'" class="grid g4 mt">
      <section class="card">
        <div class="ch"><span class="ico-tile acc"><Icon name="mail" :size="15" /></span><h3>{{ t('sharing.advice.askTitle') }}</h3></div>
        <div class="cb"><p class="muted small">{{ t('sharing.advice.ask') }}</p></div>
      </section>
      <section class="card">
        <div class="ch"><span class="ico-tile acc"><Icon name="refresh" :size="15" /></span><h3>{{ t('sharing.advice.replaceTitle') }}</h3></div>
        <div class="cb"><p class="muted small">{{ t('sharing.advice.replace') }}</p></div>
      </section>
      <section class="card">
        <div class="ch"><span class="ico-tile acc"><Icon name="key" :size="15" /></span><h3>{{ t('sharing.advice.keysTitle') }}</h3></div>
        <div class="cb"><p class="muted small">{{ t('sharing.advice.keys') }}</p></div>
      </section>
      <section class="card">
        <div class="ch"><span class="ico-tile acc"><Icon name="users" :size="15" /></span><h3>{{ t('sharing.advice.lowerTitle') }}</h3></div>
        <div class="cb"><p class="muted small">{{ t('sharing.advice.lower') }}</p></div>
      </section>
    </div>
  </div>
</template>

<style scoped>
/* The history table's columns keep their measures; the design's table
   scrolls what does not fit. */
.history-table { min-width: 1160px; table-layout: fixed; }
.history-table td { overflow-wrap: anywhere; }
.when { font-size: 12px; white-space: nowrap; }
.toolbar .search-in { flex: 1 1 260px; max-width: 380px; }
</style>
