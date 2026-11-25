<script setup>
// The tunnels: the strip of what they carry, a list by server with search,
// sorting and pages, and what can be done to each. The rows, the actions and
// the dialogs are their own; this holds the list's state and wires them.
import { ref, computed, watch, onMounted } from 'vue'
import { api } from '../lib/api.js'
import { useLive, mergeRows, useDelayed } from '../lib/live.js'
import { store, t, notify } from '../lib/store.js'
import { generalItems, nextSort, ocPayload, proven, sortRows, totals as totalsOf, wsOf, wsPayload } from '../lib/interfaces/rows.js'
import { useInterfaceActions } from '../lib/interfaces/actions.js'
import ErrorState from '../components/ErrorState.vue'
import InterfaceForm from '../components/InterfaceForm.vue'
import InterfaceDetail from '../components/InterfaceDetail.vue'
import AntIcon from '../components/AntIcon.vue'
import MoreMenu from '../components/MoreMenu.vue'
import TextDialog from '../components/TextDialog.vue'
import InterfaceStats from '../components/interfaces/InterfaceStats.vue'
import InterfaceTunnels from '../components/interfaces/InterfaceTunnels.vue'
import InterfaceCards from '../components/interfaces/InterfaceCards.vue'
import ImportDialog from '../components/interfaces/ImportDialog.vue'
import AttachDialog from '../components/interfaces/AttachDialog.vue'
import CloneDialog from '../components/interfaces/CloneDialog.vue'
import MoveDialog from '../components/interfaces/MoveDialog.vue'
import MoveClientsDialog from '../components/interfaces/MoveClientsDialog.vue'
import GroupDialog from '../components/interfaces/GroupDialog.vue'
import ConfirmDialog from '../components/ConfirmDialog.vue'
import PageSpin from '../components/PageSpin.vue'
import { useIsMobile } from '../lib/mobile.js'

const interfaces = ref([])
// The servers a tunnel can be put on. An install that never added one has
// only its own, and the form then says so instead of offering a choice of one.
const nodeList = ref([])
const busy = ref(false)
// A failed load and an empty list are not the same thing, and until now this
// page told an operator whose connection had dropped that they had no
// interfaces -- and offered to create one. On a page whose rows are live
// customer tunnels, that is an invitation to build a duplicate.
const loadError = ref(null)
const formRef = ref(null)
const loading = ref(true)
const formFor = ref(null) // null = closed, {} = create, { iface } = edit
const detailFor = ref(null)
const selected = ref(new Set())
// On a phone the table becomes a list of cards, as the classic panel's inbounds do.
const isMobile = useIsMobile()

const nf = (n) => Number(n || 0).toLocaleString(store.locale)

// Their search box beside the buttons: name, port, protocol.
const search = ref('')

// Two kinds of entries share this page, and they get two tabs and no
// more: this panel's own tunnels under WireGuard, and the inbounds
// adopted from a 3x-ui or Pasarguard panel (protocol xray) under their
// own tab -- another panel's inbounds are not tunnels of this one, and
// a page of them buries the five tunnels the page is mostly opened for.
const KIND_KEY = 'wui.interfaces.kind'
const kind = ref(readKind())
function readKind() {
  try { return localStorage.getItem(KIND_KEY) === 'sanaei' ? 'sanaei' : 'wireguard' } catch { return 'wireguard' }
}
watch(kind, (v) => {
  page.value = 1
  // A selection belongs to the tab it was made on: left over, a bulk
  // delete would reach tunnels of the other kind.
  selected.value = new Set()
  try { localStorage.setItem(KIND_KEY, v) } catch { /* private window */ }
})
const isPanel = (i) => i.protocol === 'xray'
const wgCount = computed(() => interfaces.value.filter((i) => !isPanel(i)).length)
const panelCount = computed(() => interfaces.value.filter(isPanel).length)
const hasPanel = computed(() => panelCount.value > 0)
// The panel tab picked while its panel is gone leaves nothing to show.
watch(hasPanel, (has) => { if (!has && kind.value === 'sanaei') kind.value = 'wireguard' })
const onKind = computed(() => interfaces.value.filter((i) => (kind.value === 'sanaei' ? isPanel(i) : !isPanel(i))))
// Which server's tunnels to show: a filter over the rows already
// loaded, for finding one node's tunnels without searching by name.
const serverFilter = ref('')
const serverChips = computed(() => {
  const chips = []
  if (onKind.value.some((i) => i.nodeLocal)) chips.push({ key: 'local', label: t('iface.col.localPanel') })
  const names = [...new Set(onKind.value.filter((i) => !i.nodeLocal && i.nodeName).map((i) => i.nodeName))]
  for (const name of names) chips.push({ key: name, label: name })
  return chips
})
const visible = computed(() => {
  let rows = onKind.value
  if (serverFilter.value === 'local') rows = rows.filter((i) => i.nodeLocal)
  else if (serverFilter.value) rows = rows.filter((i) => i.nodeName === serverFilter.value)
  const q = search.value.trim().toLowerCase()
  if (!q) return rows
  return rows.filter((i) =>
    [i.name, String(i.listenPort), i.protocol, i.endpointHost, i.nodeName].some((v) => String(v || '').toLowerCase().includes(q)),
  )
})
// The live speed across the tab, for the strip: the rows carry their
// own current rates, and the strip shows their sum.
const liveSpeed = computed(() =>
  onKind.value.reduce(
    (a, i) => ({ up: a.up + (i.speed?.up || 0), down: a.down + (i.speed?.down || 0) }),
    { up: 0, down: 0 },
  ),
)
// The order, chosen from a select now that the cards have no sortable
// headers: picking a column sets it, the button beside it turns it over.
function setSortKey(key) {
  sort.value = key ? nextSort({ key: '', dir: '' }, key) : { key: '', dir: '' }
}
function flipSortDir() {
  sort.value = { key: sort.value.key, dir: sort.value.dir === 'asc' ? 'desc' : 'asc' }
}

// The order the headers set, and the pages: the size from the settings
// page, and no bar at all when everything fits on one page.
const sort = ref({ key: '', dir: '' })
const sorted = computed(() => sortRows(visible.value, sort.value))
const page = ref(1)
const pageSize = computed(() => (store.panel.pageSize > 0 ? store.panel.pageSize : sorted.value.length || 1))
const pageCount = computed(() => Math.max(1, Math.ceil(sorted.value.length / pageSize.value)))
// Clamped from a watcher, not inside the computed: a filter that shrinks
// the list must not write to the page while the page is being read.
watch(pageCount, (n) => { if (page.value > n) page.value = n })
const paged = computed(() => {
  const start = (Math.min(page.value, pageCount.value) - 1) * pageSize.value
  return sorted.value.slice(start, start + pageSize.value)
})
const pageNumbers = computed(() => Array.from({ length: pageCount.value }, (_, i) => i + 1))

// The dialogs, while open.
const textModal = ref(null) // { title, text, file }
const importOpen = ref(false)
const cloning = ref(null) // the tunnel being copied
const attach = ref(null) // { iface, list }
const move = ref(null) // { iface, kind }
const moving = ref(null) // the tunnel whose customers are being moved off it
const grouping = ref(null) // { iface, names }

// What an operator does to tunnels, and the menus that offer it.
const { pending, setEnabled, ask, runConfirmed, bulkDelete, pickRow, pickGeneral } = useInterfaceActions({
  interfaces, selected, busy, load,
  open: {
    text: (m) => (textModal.value = m),
    importer: () => (importOpen.value = true),
    clone: (i) => (cloning.value = i),
    attach: (i, list) => (attach.value = { iface: i, list }),
    move: (i, kind) => (move.value = { iface: i, kind }),
    moveClients: (i) => (moving.value = i),
    group: (i, names) => (grouping.value = { iface: i, names }),
  },
})

async function load(quiet = false) {
  if (!quiet) loading.value = true
  try {
    const fresh = await api.interfaces({ background: quiet })
    loadError.value = null
    interfaces.value = quiet ? mergeRows(interfaces.value, fresh, pending.value) : fresh
    const ids = new Set(interfaces.value.map((i) => i.id))
    selected.value = new Set([...selected.value].filter((id) => ids.has(id)))
  } catch (err) {
    // A poll that fails leaves the rows that are already on screen alone; a
    // first load that fails has nothing to leave, so it says so.
    if (!quiet) {
      if (!interfaces.value.length) loadError.value = err
      else notify(err.message, 'error')
    }
  } finally {
    loading.value = false
  }
}

onMounted(load)
onMounted(async () => {
  try {
    nodeList.value = await api.get('/api/nodes', { background: true })
  } catch {
    // Not worth a message: the form falls back to this server, which is
    // where every tunnel goes on a panel with no nodes anyway.
    nodeList.value = []
  }
})

// What these rows carry -- the traffic, the speed, how much of the address
// pool is spoken for -- changes constantly. An interface that has just been
// created also takes a moment to come up, and this is what shows it happening.
const showSkeleton = useDelayed(computed(() => loading.value && !interfaces.value.length))
useLive(load, {
  every: 5000,
  busy: () => !!formFor.value || !!detailFor.value || !!ask.value,
})

const totals = computed(() => totalsOf(interfaces.value))

// The header checkbox is for the rows on this tab, not the whole page: the
// other tab's rows are not on screen, and ticking it used to select — and
// bulk-delete — tunnels the operator never saw checked.
const allSelected = computed(() => !!visible.value.length && visible.value.every((i) => selected.value.has(i.id)))
function toggleAll(checked) {
  const next = new Set(selected.value)
  if (checked) visible.value.forEach((i) => next.add(i.id))
  else visible.value.forEach((i) => next.delete(i.id))
  selected.value = next
}
function toggleOne(id, checked) {
  const next = new Set(selected.value)
  checked ? next.add(id) : next.delete(id)
  selected.value = next
}

// A WireGuard tunnel's transport as the form asks for it: plain UDP, or a
// stealth one and its port, and anchor's, drift's, relay's or bare's name, path and
// decoy. AmneziaWG has only plain UDP.
function wgTransportOf(input) {
  if (input.mode === 'amnezia' || !input.transport || input.transport === 'udp') return { transport: 'udp', transportPort: 0 }
  const out = { transport: input.transport, transportPort: Number(input.transportPort) || 0 }
  if (proven(input.transport) || input.transport === 'mirror') out.transportWs = wsPayload(input.ws)
  return out
}
function wgTransportChanged(input, iface) {
  const want = wgTransportOf(input)
  if (want.transport !== (iface.stealth?.transport || 'udp') || want.transportPort !== (iface.stealth?.port || 0)) return true
  // An empty path or cookie keeps the one there is, so it is no change.
  if (!want.transportWs) return false
  const had = wsPayload(wsOf(iface.stealth))
  const kept = (k) => (k === 'path' || k === 'cookie') && want.transportWs[k] === ''
  return Object.keys(want.transportWs).some((k) => !kept(k) && want.transportWs[k] !== had[k])
}

// The advanced WireGuard overrides, as the server takes them: blanks become
// zeros, which it reads as "use the default". A zero keepalive and MTU and an
// empty AllowedIPs are exactly the defaults, so clearing a field restores them.
function wgAdvancedForSend(a) {
  return {
    keepalive: Number(a?.keepalive) || 0,
    allowedIps: (a?.allowedIps || '').trim(),
    mtu: Number(a?.mtu) || 0,
    postUp: (a?.postUp || '').trim(),
    postDown: (a?.postDown || '').trim(),
  }
}
function wgAdvancedNonEmpty(a) {
  const v = wgAdvancedForSend(a)
  return !!(v.keepalive || v.mtu || v.allowedIps || v.postUp || v.postDown)
}
function wgAdvancedChanged(input, iface) {
  const want = wgAdvancedForSend(input.wgAdvanced)
  const had = wgAdvancedForSend(iface.wgAdvanced)
  return (
    want.keepalive !== had.keepalive ||
    want.allowedIps !== had.allowedIps ||
    want.mtu !== had.mtu ||
    want.postUp !== had.postUp ||
    want.postDown !== had.postDown
  )
}

async function submitForm(input) {
  try {
    if (formFor.value?.iface) {
      await api.updateInterface(formFor.value.iface.id, {
        enabled: input.enabled,
        name: input.name,
        listenPort: Number(input.listenPort),
        subnet: input.subnet,
        ...(input.protocol === 'wireguard' && input.mode ? { mode: input.mode } : {}),
        endpointHost: input.endpointHost,
        mtu: Number(input.mtu),
        dns: input.dns,
        natInterface: input.natInterface,
        // Only sent for OpenVPN, and only when it actually moved: the server
        // refuses the field on a WireGuard interface rather than ignoring it.
        ...(input.protocol === 'openvpn' && input.transport ? { transport: input.transport } : {}),
        // Only when it changed: a new key ends every session on the tunnel.
        ...(input.protocol === 'l2tp' && input.psk && input.psk !== (formFor.value.iface.l2tp?.psk || '') ? { psk: input.psk } : {}),
        // The same for routers without IPsec: a change restarts the tunnel.
        ...(input.protocol === 'l2tp' && input.l2tpPlain !== !!formFor.value.iface.l2tp?.allowPlain ? { l2tpPlain: input.l2tpPlain } : {}),
        ...(input.protocol === 'ikev2' ? { ikev2Modes: input.ikev2Modes } : {}),
        ...(input.protocol === 'openconnect' ? { openconnect: ocPayload(input.oc) } : {}),
        // Only when it moved; off is a port of zero.
        // Only when it moved: a device needs its file again for a new one.
        ...(input.protocol === 'wireguard' && wgTransportChanged(input, formFor.value.iface) ? wgTransportOf(input) : {}),
        // Only when it moved: a new profile means new files for everyone.
        ...(input.mode === 'amnezia' && input.awg && JSON.stringify(input.awg) !== JSON.stringify(formFor.value.iface.awg || {}) ? { awg: input.awg } : {}),
        // Keepalive, AllowedIPs and client MTU, only when they moved: each
        // change means a device needs its file again.
        ...(input.protocol === 'wireguard' && wgAdvancedChanged(input, formFor.value.iface) ? { wgAdvanced: wgAdvancedForSend(input.wgAdvanced) } : {}),
      })
      notify(t('interface.updated'), 'success')
    } else {
      // transport is OpenVPN's alone and the server refuses it elsewhere, so
      // a WireGuard interface must not carry the form's default along with it.
      // The same for psk, which is L2TP's.
      // eslint-disable-next-line no-unused-vars -- taken out of rest, sent by protocol below
      const { transport, transportPort, ws, psk, l2tpPlain, ikev2Modes, awg, awgLevel, awgMode, oc, xray, wgAdvanced, ...rest } = input
      if (xray) {
        // An inbound on a 3x-ui or Pasarguard node, made there as that
        // panel's own form would make it.
        await api.createInterface({
          name: input.name, nodeId: input.nodeId, protocol: 'xray', listenPort: Number(input.listenPort), xray,
        })
        notify(t('interface.created'), 'success')
        formFor.value = null
        await load()
        return
      }
      await api.createInterface({
        ...rest,
        listenPort: Number(input.listenPort),
        mtu: Number(input.mtu),
        ...(input.protocol === 'openvpn' && transport ? { transport } : {}),
        ...(input.protocol === 'l2tp' && psk ? { psk } : {}),
        ...(input.protocol === 'l2tp' && l2tpPlain ? { l2tpPlain: true } : {}),
        ...(input.protocol === 'ikev2' ? { ikev2Modes } : {}),
        ...(input.protocol === 'openconnect' ? { openconnect: ocPayload(oc) } : {}),
        ...(input.protocol === 'wireguard' && wgTransportOf(input).transport !== 'udp' ? wgTransportOf(input) : {}),
        // The profile the form shows, as drawn for this server and perhaps
        // changed: what is created is what the operator saw.
        ...(input.protocol === 'wireguard' && input.mode === 'amnezia' ? (awg ? { awg } : { awgLevel }) : {}),
        // Only when the operator set one of them: a bare tunnel keeps the
        // defaults and stores nothing.
        ...(input.protocol === 'wireguard' && wgAdvancedNonEmpty(wgAdvanced) ? { wgAdvanced: wgAdvancedForSend(wgAdvanced) } : {}),
      })
      notify(t('interface.created'), 'success')
    }
    formFor.value = null
    await load()
  } catch (err) {
    // A message the server attached to a field belongs under that field, not in
    // a toast the operator has to map back to one of nine inputs themselves.
    if (err?.field) {
      formRef.value?.showError(err)
    } else {
      notify(err.message, 'error')
    }
    // Deliberately not rethrown. It has been handled; letting it escape an
    // event handler sends it to Vue, which routes it to the error boundary and
    // turns "that port is out of range" into "this page could not be
    // displayed".
  }
}
</script>

<template>
  <div class="antpage inbounds cs">
  <div class="ph">
    <div>
      <h2>{{ t('nav.interfaces') }}</h2>
      <p>{{ t('iface.lede') }}</p>
    </div>
    <div class="actions">
      <button class="btn primary" @click="formFor = {}">
        <AntIcon name="PlusOutlined" />{{ t('iface.menu.add') }}
      </button>
    </div>
  </div>

  <!-- The strip over the list: what the set has carried, and how fast
       it is moving now. -->
  <InterfaceStats :totals="totals" :rows="onKind" :live="liveSpeed" />

  <!-- Their list Card: General Actions, the search, the order, and --
       with rows picked -- the count and a Delete. -->
  <div class="card calm" style="margin-top: 14px">
    <div class="toolbar" style="border-bottom: 1px solid var(--line-soft)">
      <MoreMenu :items="generalItems()" :compact="isMobile" button-class="btn" icon="MenuOutlined" :label="t('iface.menu.general')" @pick="pickGeneral" />
      <label class="ainput">
        <span class="ainput-prefix"><AntIcon name="SearchOutlined" /></span>
        <input v-model="search" type="text" :placeholder="t('iface.menu.search')" :aria-label="t('iface.menu.search')" />
        <button v-if="search" type="button" class="ainput-clear" :aria-label="t('action.cancel')" @click="search = ''"><AntIcon name="CloseCircleFilled" /></button>
      </label>
      <template v-if="selected.size">
        <span class="atag blue closable">
          {{ t('client.menu.selectedCount').replace('{count}', nf(selected.size)) }}
          <button type="button" class="atag-close" :aria-label="t('action.cancel')" @click="selected = new Set()"><AntIcon name="CloseOutlined" /></button>
        </span>
        <button class="btn danger" @click="bulkDelete">
          <AntIcon name="DeleteOutlined" /><span v-if="!isMobile">{{ t('action.delete') }}</span>
        </button>
      </template>
      <span class="spacer"></span>
      <select class="aselect" style="width: auto" :value="sort.key" :aria-label="t('client.sort.label')" @change="setSortKey($event.target.value)">
        <option value="">{{ t('client.sort.label') }}</option>
        <option value="id">ID</option>
        <option value="remark">{{ t('iface.col.remark') }}</option>
        <option value="node">{{ t('iface.col.node') }}</option>
        <option value="port">{{ t('interface.port') }}</option>
        <option value="protocol">{{ t('client.protocol') }}</option>
        <option value="clients">{{ t('nav.clients') }}</option>
        <option value="traffic">{{ t('client.traffic') }}</option>
        <option value="speed">{{ t('client.speed') }}</option>
      </select>
      <button v-if="sort.key" class="btn sm" :aria-label="t('client.sort.label')" @click="flipSortDir">{{ sort.dir === 'asc' ? '↑' : '↓' }}</button>
    </div>

    <div class="cb" style="padding-top: 12px">
      <!-- One chip per server with tunnels on this tab: a way to look at
           one node's tunnels without typing its name into the search. -->
      <div v-if="serverChips.length > 1" class="chips" style="margin-bottom: 12px">
        <button class="fchip" :class="{ on: !serverFilter }" @click="serverFilter = ''">{{ t('common.all') }}</button>
        <button v-for="chip in serverChips" :key="chip.key" class="fchip" :class="{ on: serverFilter === chip.key }" @click="serverFilter = chip.key">{{ chip.label }}</button>
      </div>
      <div v-if="hasPanel" class="atabs-nav server-tabs"><div class="atabs-list" role="tablist">
        <button class="atab" role="tab" :class="{ active: kind === 'wireguard' }" :aria-selected="kind === 'wireguard'" @click="kind = 'wireguard'">
          <AntIcon name="ClusterOutlined" /><span>{{ t('protocol.wireguard') }}</span><span class="atag count">{{ nf(wgCount) }}</span>
        </button>
        <button class="atab" role="tab" :class="{ active: kind === 'sanaei' }" :aria-selected="kind === 'sanaei'" @click="kind = 'sanaei'">
          <AntIcon name="ApiOutlined" /><span>{{ t('iface.server.xui') }}</span><span class="atag count">{{ nf(panelCount) }}</span>
        </button>
      </div></div>
      <ErrorState v-if="loadError" :error="loadError" @retry="load()" />

      <PageSpin v-else-if="showSkeleton" />
      <div v-else-if="loading" class="empty"></div>

      <InterfaceCards v-else-if="isMobile" :rows="paged" :selected="selected" :all-selected="allSelected" :pending="pending"
        @toggle="toggleOne" @toggle-all="toggleAll" @enable="setEnabled" @pick="pickRow">
        <ul v-if="pageCount > 1" class="apagination">
          <li><button class="apage" :disabled="page <= 1" :aria-label="t('action.previous')" @click="page--">‹</button></li>
          <li v-for="n in pageNumbers" :key="n"><button class="apage" :class="{ active: n === page }" @click="page = n">{{ n }}</button></li>
          <li><button class="apage" :disabled="page >= pageCount" :aria-label="t('action.next')" @click="page++">›</button></li>
        </ul>
      </InterfaceCards>
      <InterfaceTunnels v-else :rows="paged" :selected="selected" :all-selected="allSelected" :pending="pending"
        @toggle="toggleOne" @toggle-all="toggleAll" @enable="setEnabled" @pick="pickRow"
        @edit="(i) => (formFor = { iface: i })">
        <ul v-if="pageCount > 1" class="apagination">
          <li><button class="apage" :disabled="page <= 1" :aria-label="t('action.previous')" @click="page--">‹</button></li>
          <li v-for="n in pageNumbers" :key="n"><button class="apage" :class="{ active: n === page }" @click="page = n">{{ n }}</button></li>
          <li><button class="apage" :disabled="page >= pageCount" :aria-label="t('action.next')" @click="page++">›</button></li>
        </ul>
      </InterfaceTunnels>
    </div>
  </div>
  </div>

  <MoveDialog v-if="move" :iface="move.iface" :kind="move.kind" :interfaces="interfaces" @close="move = null" @done="load()" />
  <MoveClientsDialog v-if="moving" :iface="moving" :interfaces="interfaces" @close="moving = null" @done="load()" />
  <GroupDialog v-if="grouping" :iface="grouping.iface" :names="grouping.names" @close="grouping = null" @done="load()" />

  <TextDialog v-if="textModal" :title="textModal.title" :text="textModal.text" :file="textModal.file" @close="textModal = null" />
  <ImportDialog v-if="importOpen" @close="importOpen = false" @imported="load()" />
  <AttachDialog v-if="attach" :iface="attach.iface" :list="attach.list" @close="attach = null" @attached="load()" />

  <InterfaceForm
    ref="formRef"
    v-if="formFor"
    :iface="formFor.iface"
    :nodes="nodeList"
    :on-submit="submitForm"
    @close="formFor = null"
  />

  <InterfaceDetail v-if="detailFor" :iface="detailFor" @close="detailFor = null" />

  <ConfirmDialog
    :open="!!ask"
    :title="ask?.title || ''"
    :body="ask?.body || ''"
    :subject="ask?.subject || ''"
    :consequences="ask?.consequences || []"
    :confirm-label="ask?.confirmLabel || ''"
    :require-text="ask?.requireText || ''"
    :busy="busy"
    @confirm="runConfirmed"
    @cancel="ask = null"
  />

  <CloneDialog v-if="cloning" :from="cloning" :interfaces="interfaces" @close="cloning = null" @cloned="load()" />
</template>

<style>
/* Keyed on the page's root rather than scoped, so the components it is
   made of are styled as when their markup was the page's. */
/* Their column widths, exactly */
.inbounds .w-id { width: 60px; }
.inbounds .w-menu { width: 70px; }
.inbounds .w-enable { width: 80px; }
.inbounds .w-remark { width: 90px; }
.inbounds .w-node { width: 130px; }
.inbounds .server-tabs .atag.count { margin-inline-start: 6px; }
.inbounds .w-port { width: 80px; }
.inbounds .w-proto { width: 190px; }
.inbounds .w-clients { width: 200px; }
.inbounds .w-itraffic { width: 140px; }
.inbounds .w-speed { width: 216px; }
.inbounds .w-dur { width: 100px; }
.inbounds .atable th.center, .inbounds .atable td.center { text-align: center; }
.inbounds .atable th.right, .inbounds .atable td.right { text-align: end; }
.inbounds .action-buttons {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 4px;
}
.inbounds .card-empty {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 8px;
  padding: 24px 12px;
  color: var(--muted);
}
.inbounds .card-empty .anticon { margin-bottom: 8px; }

.inbounds .strip {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(180px, 1fr));
  gap: 1px;
  background: var(--line-soft);
  margin-bottom: 16px;
  overflow: hidden;
}
.inbounds .strip-item {
  background: var(--surface);
  padding: 14px 18px;
  display: flex;
  flex-direction: column;
  gap: 5px;
}
.inbounds .strip-label {
  display: flex;
  align-items: center;
  gap: 7px;
  font-size: var(--t-xs);
  color: var(--muted);
}
.inbounds .strip-value {
  font-size: var(--t-lg);
  font-weight: 600;
  line-height: 1.15;
}

.inbounds .actionbar {
  display: flex;
  align-items: center;
  gap: 9px;
  flex-wrap: wrap;
  margin-bottom: 16px;
}
.inbounds .selcount {
  color: var(--muted);
  margin-inline-start: 8px;
}

.inbounds .name {
  color: var(--ink);
  font-weight: 600;
}
.inbounds .tags {
  display: flex;
  gap: 5px;
  flex-wrap: wrap;
}
.inbounds .linkish {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  border: none;
  background: transparent;
  color: var(--ink-2);
  font: inherit;
  font-size: var(--t-sm);
  cursor: pointer;
  padding: 0;
}
.inbounds .linkish:hover {
  color: var(--accent-hover);
}
</style>
