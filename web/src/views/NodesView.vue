<script setup>
// The servers this panel runs customers on: the figures across the top, a
// row each -- a card on a phone -- and what can be done to one. How a node
// reads is lib/nodes/rows.js; the rows, the form and the token dialog are
// their own; this holds the list and what is mid-request.
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { api } from '../lib/api.js'
import { mergeRows, useDelayed } from '../lib/live.js'
import { t, notify } from '../lib/store.js'
import { ago, takesImport, totals as totalsOf } from '../lib/nodes/rows.js'
import { nodePlace } from '../lib/nodes/geo.js'
import { WORLD_DOTS } from '../lib/nodes/worldDots.js'
import Icon from '../components/Icon.vue'
import ErrorState from '../components/ErrorState.vue'
import ConfirmDialog from '../components/ConfirmDialog.vue'
import PageSpin from '../components/PageSpin.vue'
import NodeImportDialog from '../components/NodeImportDialog.vue'
import NodeGrid from '../components/nodes/NodeGrid.vue'
import NodeForm from '../components/nodes/NodeForm.vue'
import TokenDialog from '../components/nodes/TokenDialog.vue'
import AuthorityDialog from '../components/nodes/AuthorityDialog.vue'

const nodes = ref([])
const loading = ref(true)

// Declared here, after the state it reads: useDelayed watches with `immediate`
// and evaluates its source during setup.
const showSkeleton = useDelayed(computed(() => loading.value && !nodes.value.length))
const loadError = ref(null)
const busy = ref(false)
const formFor = ref(null) // {} to add, { node } to edit
const issuing = ref(false)
// A 3x-ui or Pasarguard node whose own users are being brought in.
const importing = ref(null)
let timer = null

onMounted(() => {
  load()
  // The panel probes on its own every half minute; this only re-reads what it
  // already found, so it can be frequent without costing a remote request.
  timer = setInterval(() => load(true), 15_000)
})
onBeforeUnmount(() => clearInterval(timer))

async function load(quiet = false) {
  try {
    const fresh = await api.get('/api/nodes', { background: quiet })
    nodes.value = quiet ? mergeRows(nodes.value, fresh, pending.value) : fresh
    loadError.value = null
  } catch (e) {
    loadError.value = e
  } finally {
    loading.value = false
  }
}

const totals = computed(() => totalsOf(nodes.value))

// The fleet strip's fourth figure and first: what kinds of panel the
// fleet is made of, and which node has been silent longest.
const authorityOpen = ref(false)
const kindsLine = computed(() => {
  const counts = { wui: 0, xui: 0, pasarguard: 0 }
  for (const n of nodes.value) {
    if (n.kind === 'xui') counts.xui++
    else if (n.kind === 'pasarguard') counts.pasarguard++
    else counts.wui++
  }
  const parts = []
  if (counts.wui) parts.push(`${counts.wui} W-UI`)
  if (counts.xui) parts.push(`${counts.xui} 3x-ui`)
  if (counts.pasarguard) parts.push(`${counts.pasarguard} Pasarguard`)
  return parts.join(' · ')
})
const firstOffline = computed(
  () => nodes.value.find((n) => n.kind !== 'local' && n.enabled && !n.reachable) || null,
)

// The fleet map. The dotted world is the design's own artwork; the
// markers are the nodes whose names carry a flag this panel knows
// (lib/nodes/geo.js) — a node without one is listed below the map and
// simply has no dot on it. The panel's own node anchors the arcs.
const dotsSvg = WORLD_DOTS.split(' ')
  .map((p) => {
    const [x, y] = p.split(',')
    return `<circle class="land" cx="${x}" cy="${y}" r=".28"/>`
  })
  .join('')
const markers = computed(() => {
  const out = []
  for (const n of nodes.value) {
    const place = nodePlace(n.name)
    if (n.kind === 'local') out.push({ n, main: true, place: place || { x: 52.5, y: 28.4 } })
    else if (place) out.push({ n, main: false, place })
  }
  return out
})
const markerOff = (n) => n.kind !== 'local' && (!n.enabled || !n.reachable)
const markerSub = (n) => {
  if (n.kind === 'local') return t('node.running')
  if (!n.enabled) return t('status.disabled')
  if (n.reachable) return `${n.latencyMs || 0} ms`
  return n.lastSeenAt ? ago(n.lastSeenAt) : t('node.noAnswer')
}
const shortName = (name) => name.replace(/^[^A-Za-z0-9\u0600-\u06FF]+/, '').trim() || name
const arcs = computed(() => {
  const main = markers.value.find((m) => m.main)
  if (!main) return []
  return markers.value
    .filter((m) => !m.main)
    .map((m) => {
      const mx = (main.place.x + m.place.x) / 2
      const my = Math.min(main.place.y, m.place.y) - 9
      return {
        key: m.n.id,
        bad: markerOff(m.n),
        d: `M ${main.place.x} ${main.place.y} Q ${mx} ${my} ${m.place.x} ${m.place.y}`,
      }
    })
})

async function saved(made) {
  formFor.value = null
  await load()
  // A 3x-ui or Pasarguard panel with users of its own: offered to bring
  // them in at once. A W-UI panel is asked when the operator chooses.
  if (made && takesImport(made) && made.kind !== 'remote') importing.value = made
}

// Asking a node to update its own panel.
//
// The answer is passed back whole: "this build carries no release-signing key"
// is something an operator can act on, and is a different problem from the node
// not answering at all.
function askNodeUpdate(node) {
  ask.value = {
    title: t('update.askNodeTitle'),
    subject: node.name,
    body: t('update.askNodeBody'),
    confirmLabel: t('update.install'),
    run: async () => {
      const res = await api.post(`/api/nodes/${node.id}/update`)
      if (res?.started) {
        notify(t('update.nodeStarted').replace('{v}', res.to || ''), 'success')
      } else if (res?.updated) {
        notify(t('update.nodeUpdated').replace('{v}', res.to || ''), 'success')
      } else {
        notify(res?.notice || t('update.upToDate'), 'success')
      }
      await load()
    },
  }
}

// Which rows are mid-request, by node id.
//
// A probe crosses the network to another server: it can take seconds, and it
// can time out. Marking the whole page busy meant the operator could not probe
// a second node while the first was still answering, and the row they had just
// clicked looked exactly like the ones they had not.
const pending = ref(new Set())

function hold(id) {
  pending.value = new Set(pending.value).add(id)
}
function release(id) {
  const next = new Set(pending.value)
  next.delete(id)
  pending.value = next
}

async function probe(n) {
  hold(n.id)
  try {
    await api.post(`/api/nodes/${n.id}/probe`)
    // Quiet: the row's own spinner already says something is happening, and a
    // second indicator in the header for the same click reads as two things
    // going on at once.
    await load(true)
  } catch (e) {
    notify(e.message, 'error')
  } finally {
    release(n.id)
  }
}

// The box ticks now; the panel catches up.
async function toggle(n, enabled) {
  const was = n.enabled
  if (was === enabled) return
  n.enabled = enabled
  hold(n.id)
  try {
    const updated = await api.patch(`/api/nodes/${n.id}`, {
      name: n.name,
      address: n.address,
      enabled,
    })
    Object.assign(n, updated)
  } catch (e) {
    // Put the box back where it was rather than leaving it showing a state the
    // panel never reached.
    n.enabled = was
    notify(e.message, 'error')
  } finally {
    release(n.id)
    load(true)
  }
}

const ask = ref(null)

function remove(n) {
  ask.value = {
    title: t('node.confirmDeleteTitle'),
    body: t('node.confirmDeleteBody'),
    subject: n.name,
    // Worth saying plainly: this is a change to this panel's list, not to the
    // server at the other end. Its customers keep working.
    consequences: [t('node.consequenceRemote'), t('node.consequenceToken')],
    confirmLabel: t('action.delete'),
    run: async () => {
      try {
        await api.del(`/api/nodes/${n.id}`)
        await load()
      } catch (e) {
        notify(e.message, 'error')
      }
    },
  }
}

async function runConfirmed() {
  const spec = ask.value
  if (!spec) return
  busy.value = true
  try {
    await spec.run()
  } finally {
    busy.value = false
    ask.value = null
  }
}

// What a row or a card asks for.
const rowEvents = {
  onProbe: probe,
  onUpdate: askNodeUpdate,
  onImport: (n) => (importing.value = n),
  onEdit: (n) => (formFor.value = { node: n }),
  onRemove: remove,
  onToggle: toggle,
}
</script>

<template>
  <div class="cs">
    <div class="ph">
      <div>
        <h2>{{ t('nav.nodes') }}</h2>
        <p>{{ t('node.lede') }}</p>
      </div>
      <div class="actions">
        <button class="btn" @click="authorityOpen = true">
          <Icon name="shield" :size="14" />{{ t('node.authority') }}
        </button>
        <button class="btn" @click="issuing = true">
          <Icon name="key" :size="14" />{{ t('node.issueToken') }}
        </button>
        <button class="btn primary" @click="formFor = {}">
          <Icon name="plus" :size="14" />{{ t('node.add') }}
        </button>
      </div>
    </div>

    <!-- The fleet in four figures: how many, how many answering, how
         many silent and which one, and how far away they read. -->
    <div class="grid g4 mb2">
      <div class="card stat">
        <div class="lbl">
          <span class="ico-tile" style="width: 26px; height: 26px; border-radius: 8px"><Icon name="server" :size="14" /></span>
          {{ t('node.total') }}
        </div>
        <div class="val">{{ totals.all }}</div>
        <div class="foot faint">{{ kindsLine }}</div>
      </div>
      <div class="card stat">
        <div class="lbl">
          <span class="ico-tile ok" style="width: 26px; height: 26px; border-radius: 8px"><Icon name="check" :size="14" /></span>
          {{ t('node.online') }}
        </div>
        <div class="val">{{ totals.online }}</div>
      </div>
      <div class="card stat" :class="{ hot: totals.offline > 0 }">
        <div class="lbl">
          <span class="ico-tile bad" style="width: 26px; height: 26px; border-radius: 8px"><Icon name="alert" :size="14" /></span>
          {{ t('node.offline') }}
        </div>
        <div class="val">{{ totals.offline }}</div>
        <div v-if="firstOffline" class="foot faint">
          {{ firstOffline.name }} · {{ firstOffline.lastSeenAt ? ago(firstOffline.lastSeenAt) : t('node.noAnswer') }}
        </div>
      </div>
      <div class="card stat">
        <div class="lbl">
          <span class="ico-tile acc" style="width: 26px; height: 26px; border-radius: 8px"><Icon name="clock" :size="14" /></span>
          {{ t('node.avgLatency') }}
        </div>
        <div class="val ltr">{{ totals.latency === null ? '—' : totals.latency + ' ms' }}</div>
        <div class="foot faint">{{ t('node.latencyFoot') }}</div>
      </div>
    </div>

    <!-- The fleet on the design's dotted world: a marker for every node
         whose name carries a known flag, an arc from this panel to each. -->
    <section class="card calm mb2">
      <div class="ch">
        <h3>{{ t('node.fleet') }}</h3>
        <span class="sub">{{ t('node.fleetSub') }}</span>
        <div class="row tight spacer">
          <span class="small"><span class="ddot info"></span> {{ t('node.thisPanel') }}</span>
          <span class="small"><span class="ddot"></span> {{ t('node.online') }}</span>
          <span class="small"><span class="ddot bad"></span> {{ t('node.offline') }}</span>
        </div>
      </div>
      <div class="map">
        <svg viewBox="0 0 100 50" preserveAspectRatio="xMidYMid slice" aria-hidden="true" v-html="dotsSvg"></svg>
        <svg viewBox="0 0 100 100" preserveAspectRatio="none" aria-hidden="true">
          <path v-for="a in arcs" :key="a.key" class="arc" :class="{ bad: a.bad }" :d="a.d" />
        </svg>
        <div
          v-for="m in markers"
          :key="m.n.id"
          class="map-node"
          :class="{ main: m.main, off: markerOff(m.n) }"
          :style="{ left: m.place.x + '%', top: m.place.y + '%' }"
        >
          <span class="halo"></span><span class="core"></span>
          <span class="lbl">{{ shortName(m.n.name) }}<small>{{ markerSub(m.n) }}</small></span>
        </div>
      </div>
    </section>

    <div v-if="showSkeleton || loading || loadError" class="card">
      <PageSpin v-if="showSkeleton" />
      <div v-else-if="loading" class="empty"></div>
      <ErrorState v-else-if="loadError" :error="loadError" @retry="load" />
    </div>
    <NodeGrid v-else :nodes="nodes" :pending="pending" v-bind="rowEvents" />
  </div>

  <AuthorityDialog v-if="authorityOpen" @close="authorityOpen = false" />
  <NodeForm v-if="formFor" :node="formFor.node || null" @close="formFor = null" @saved="saved" />
  <TokenDialog v-if="issuing" @close="issuing = false" />
  <NodeImportDialog v-if="importing" :node="importing" @close="importing = null" />

  <ConfirmDialog
    :open="!!ask"
    :title="ask?.title || ''"
    :body="ask?.body || ''"
    :subject="ask?.subject || ''"
    :consequences="ask?.consequences || []"
    :confirm-label="ask?.confirmLabel || ''"
    :busy="busy"
    @confirm="runConfirmed"
    @cancel="ask = null"
  />
</template>
