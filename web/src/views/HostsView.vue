<script setup>
import { computed, onMounted, ref } from 'vue'
import { api } from '../lib/api.js'
import { useDelayed } from '../lib/live.js'
import { store, t, notify } from '../lib/store.js'
import AntIcon from '../components/AntIcon.vue'
import Icon from '../components/Icon.vue'
import ConfirmDialog from '../components/ConfirmDialog.vue'
import ErrorState from '../components/ErrorState.vue'
import HostForm from '../components/HostForm.vue'
import PageSpin from '../components/PageSpin.vue'
import { useIsMobile } from '../lib/mobile.js'
import Toggle from '../components/Toggle.vue'
// the classic panel's HostList drops the button's word on a phone.
const isMobile = useIsMobile()

// The hosts page, laid out as the classic panel's HostsPage: a summary of three
// figures, then a small card whose title is the toolbar and whose body is
// the table of host groups -- one row per name, however many addresses and
// inbounds sit behind it.

const groups = ref([])
const interfaces = ref([])
const loading = ref(true)
const loadError = ref(null)
const showWait = useDelayed(computed(() => loading.value && !groups.value.length))
const formFor = ref(null) // null | {} | { group }
const ask = ref(null)
const busy = ref(false)
const selected = ref(new Set())
const nf = (n) => Number(n || 0).toLocaleString(store.locale)

const summary = computed(() => ({
  total: groups.value.length,
  enabled: groups.value.filter((g) => g.enabled).length,
  disabled: groups.value.filter((g) => !g.enabled).length,
}))
const ifaceById = computed(() => Object.fromEntries(interfaces.value.map((i) => [i.id, i])))
// The design's tag tones for the two protocol families a host can ride on.
const PROTO_COLOR = { wireguard: 'gold', openvpn: 'warn' }

async function load(quiet = false) {
  if (!quiet) loading.value = true
  try {
    const [g, i] = await Promise.all([
      api.get('/api/hosts/groups', { background: quiet }),
      api.get('/api/interfaces', { background: quiet }),
    ])
    groups.value = g || []
    interfaces.value = i || []
    loadError.value = null
    const ids = new Set(groups.value.map((x) => x.groupId))
    selected.value = new Set([...selected.value].filter((id) => ids.has(id)))
  } catch (e) {
    loadError.value = e
  } finally {
    loading.value = false
  }
}
onMounted(load)

const allSelected = computed(() => !!groups.value.length && selected.value.size === groups.value.length)
function toggleAll(on) {
  selected.value = on ? new Set(groups.value.map((g) => g.groupId)) : new Set()
}
function toggleOne(id, on) {
  const next = new Set(selected.value)
  on ? next.add(id) : next.delete(id)
  selected.value = next
}

async function setEnabled(g, on) {
  try {
    await api.post('/api/hosts/groups/bulk', { action: on ? 'enable' : 'disable', groupIds: [g.groupId] })
    g.enabled = on
  } catch (e) {
    notify(e.message, 'error')
  }
}

async function bulk(action) {
  const ids = [...selected.value]
  if (!ids.length) return
  if (action === 'delete') {
    ask.value = {
      title: t('hosts.bulkDeleteConfirm').replace('{count}', nf(ids.length)),
      confirmLabel: t('hosts.bulkDelete'),
      danger: true,
      run: async () => {
        await api.post('/api/hosts/groups/bulk', { action, groupIds: ids })
        selected.value = new Set()
        await load(true)
      },
    }
    return
  }
  try {
    await api.post('/api/hosts/groups/bulk', { action, groupIds: ids })
    await load(true)
  } catch (e) {
    notify(e.message, 'error')
  }
}

function remove(g) {
  ask.value = {
    title: t('hosts.deleteConfirmTitle').replace('{name}', g.remark),
    confirmLabel: t('action.delete'),
    danger: true,
    run: async () => {
      await api.del(`/api/hosts/groups/${encodeURIComponent(g.groupId)}`)
      await load(true)
    },
  }
}

async function runConfirmed() {
  const a = ask.value
  busy.value = true
  try {
    await a.run()
    ask.value = null
  } catch (e) {
    notify(e.message, 'error')
  } finally {
    busy.value = false
  }
}

// Check now: each address behind the entry is tried the way a customer's
// device would reach it, and the column shows the result. Nothing checks a
// host on its own, so until this is pressed the column only says that
// nothing has failed yet.
const checking = ref(null)
async function check(g) {
  // Nothing behind the entry, nothing to try: reporting success here
  // would claim a host was reached that was never probed.
  if (!(g.rowIds || []).length) {
    notify(t('hosts.nothingToCheck'), 'error')
    return
  }
  checking.value = g.groupId
  try {
    const fails = []
    let slowest = 0
    let note = ''
    for (const id of g.rowIds || []) {
      const r = await api.post(`/api/hosts/${id}/check`, {})
      if (!r.ok) fails.push(r.error || t('hosts.unreachable'))
      else {
        slowest = Math.max(slowest, r.latencyMs || 0)
        if (r.error) note = r.error
      }
    }
    if (fails.length) notify(`${g.remark}: ${[...new Set(fails)].join('; ')}`, 'error')
    else notify(`${g.remark}: ${t('hosts.reachable')} · ${slowest} ms${note ? ` — ${note}` : ''}`, 'success')
    await load(true)
  } catch (e) {
    notify(e.message, 'error')
  } finally {
    checking.value = null
  }
}

// Their move up / move down: the whole order is sent, first first.
async function move(g, dir) {
  const ids = groups.value.map((x) => x.groupId)
  const i = ids.indexOf(g.groupId)
  const j = dir === 'up' ? i - 1 : i + 1
  if (j < 0 || j >= ids.length) return
  ;[ids[i], ids[j]] = [ids[j], ids[i]]
  try {
    await api.post('/api/hosts/groups/reorder', { groupIds: ids })
    await load(true)
  } catch (e) {
    notify(e.message, 'error')
  }
}

function onSaved() {
  formFor.value = null
  load(true)
}

const popover = ref(null) // { key, items, x, y }
function showMore(key, items, e) {
  if (popover.value?.key === key) {
    popover.value = null
    return
  }
  const r = e.currentTarget.getBoundingClientRect()
  popover.value = { key, items, x: r.right, y: r.bottom + 4 }
}
function ifaceLabel(id) {
  return ifaceById.value[id]?.name || `#${id}`
}
</script>

<template>
  <div class="cs">
    <div class="ph">
      <div>
        <h2>{{ t('nav.hosts') }}</h2>
        <p>{{ t('hosts.lede') }}</p>
      </div>
      <div class="actions">
        <button class="btn primary" :disabled="!interfaces.length" @click="formFor = {}"><Icon name="plus" :size="14" /><span v-if="!isMobile">{{ t('hosts.addHost') }}</span></button>
      </div>
    </div>

    <!-- The design's three figures: how many, how many on, how many off. -->
    <div class="grid g3 mb">
      <div class="card stat">
        <div class="lbl"><span class="ico-tile"><Icon name="globe" :size="14" /></span>{{ t('hosts.summary.total') }}</div>
        <div class="val">{{ nf(summary.total) }}</div>
      </div>
      <div class="card stat">
        <div class="lbl"><span class="ico-tile ok"><Icon name="check" :size="14" /></span>{{ t('hosts.summary.enabled') }}</div>
        <div class="val">{{ nf(summary.enabled) }}</div>
      </div>
      <div class="card stat">
        <div class="lbl"><span class="ico-tile"><Icon name="pause" :size="14" /></span>{{ t('hosts.summary.disabled') }}</div>
        <div class="val">{{ nf(summary.disabled) }}</div>
      </div>
    </div>

    <section class="card calm">
      <div class="toolbar">
        <template v-if="!selected.size">
          <button class="btn primary" :disabled="!interfaces.length" @click="formFor = {}"><Icon name="plus" :size="14" /><span v-if="!isMobile">{{ t('hosts.addHost') }}</span></button>
        </template>
        <template v-else>
          <span class="selbar">
            {{ t('hosts.selectedCount').replace('{count}', nf(selected.size)) }}
            <button type="button" :aria-label="t('action.cancel')" @click="selected = new Set()"><Icon name="close" :size="12" /></button>
          </span>
          <button class="btn" @click="bulk('enable')">{{ t('hosts.bulkEnable') }}</button>
          <button class="btn" @click="bulk('disable')">{{ t('hosts.bulkDisable') }}</button>
          <button class="btn danger" @click="bulk('delete')"><Icon name="trash" :size="14" /><span>{{ t('hosts.bulkDelete') }}</span></button>
        </template>
      </div>

      <ErrorState v-if="loadError && !groups.length" :error="loadError" @retry="load()" />
      <PageSpin v-else-if="showWait" />
      <div v-else-if="loading && !groups.length" class="empty"></div>
      <div v-else class="tw">
        <table style="min-width: 900px">
          <thead>
            <tr>
              <th class="w0"><input type="checkbox" :checked="allSelected" :aria-label="t('action.selectAll')" @change="toggleAll($event.target.checked)" /></th>
              <th>{{ t('hosts.fields.actions') }}</th>
              <th>{{ t('hosts.fields.enable') }}</th>
              <th>{{ t('hosts.fields.remark') }}</th>
              <th>{{ t('hosts.fields.endpoint') }}</th>
              <th>{{ t('hosts.fields.inbound') }}</th>
              <th>{{ t('hosts.fields.reachable') }}</th>
              <th>{{ t('hosts.fields.tags') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-if="!groups.length">
              <td colspan="8">
                <div class="dempty">
                  <span class="ico-tile"><Icon name="globe" :size="22" /></span>
                  <div>{{ t('common.nothingYet') }}</div>
                </div>
              </td>
            </tr>
            <tr v-for="(g, idx) in groups" :key="g.groupId" :class="{ picked: selected.has(g.groupId), off: !g.enabled }">
              <td class="w0 keep"><input type="checkbox" :checked="selected.has(g.groupId)" :aria-label="g.remark" @change="toggleOne(g.groupId, $event.target.checked)" /></td>
              <td class="w0 keep">
                <div class="acts">
                  <button class="act" :title="t('hosts.moveUp')" :aria-label="t('hosts.moveUp')" :disabled="idx === 0" @click="move(g, 'up')"><AntIcon name="ArrowUpOutlined" /></button>
                  <button class="act" :title="t('hosts.moveDown')" :aria-label="t('hosts.moveDown')" :disabled="idx >= groups.length - 1" @click="move(g, 'down')"><AntIcon name="ArrowDownOutlined" /></button>
                  <button class="act" :title="t('hosts.checkNow')" :aria-label="t('hosts.checkNow')" :disabled="checking === g.groupId" @click="check(g)"><AntIcon :name="checking === g.groupId ? 'LoadingOutlined' : 'ThunderboltOutlined'" /></button>
                  <button class="act" :title="t('action.edit')" :aria-label="t('action.edit')" @click="formFor = { group: g }"><AntIcon name="EditOutlined" /></button>
                  <button class="act danger" :title="t('action.delete')" :aria-label="t('action.delete')" @click="remove(g)"><AntIcon name="DeleteOutlined" /></button>
                </div>
              </td>
              <td class="keep"><Toggle :model-value="g.enabled" :label="g.remark" small @update:model-value="(v) => setEnabled(g, v)" /></td>
              <td>
                <b>{{ g.remark }}</b>
                <div v-if="g.description" class="note">{{ g.description }}</div>
              </td>
              <td>
                <span v-if="!g.hosts.length" class="faint">{{ t('hosts.fields.inheritAddress') }}</span>
                <template v-else>
                  <span class="mono small" dir="ltr">{{ g.hosts[0] }}</span>
                  <span v-if="g.hosts.length > 1" class="dtag" style="cursor: pointer" @click="showMore(`h-${g.groupId}`, g.hosts, $event)">+{{ g.hosts.length - 1 }}</span>
                </template>
              </td>
              <td>
                <span v-if="!g.interfaceIds.length" class="faint">—</span>
                <template v-else>
                  <span class="dtag sq" :class="PROTO_COLOR[ifaceById[g.interfaceIds[0]]?.protocol] || ''" :title="ifaceLabel(g.interfaceIds[0])">{{ ifaceLabel(g.interfaceIds[0]) }}</span>
                  <span v-if="g.interfaceIds.length > 1" class="dtag sq" style="cursor: pointer" @click="showMore(`i-${g.groupId}`, g.interfaceIds.map(ifaceLabel), $event)">+{{ g.interfaceIds.length - 1 }}</span>
                </template>
              </td>
              <td>
                <span class="dtag" :class="g.reachable ? 'ok' : 'bad'" :title="g.lastError || ''"><span v-if="g.reachable" class="d"></span>{{ g.reachable ? t('hosts.reachable') : t('hosts.unreachable') }}</span>
              </td>
              <td>
                <template v-if="g.tags?.length"><span v-for="tag in g.tags" :key="tag" class="dtag sq" style="margin: 0 4px 4px 0">{{ tag }}</span></template>
                <span v-else class="faint">—</span>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </section>

    <!-- What a host means, per protocol family: the design's three
         explainer cards under the table. -->
    <div class="grid g3 mt">
      <section class="card">
        <div class="ch"><span class="ico-tile acc"><Icon name="shield" :size="15" /></span><h3>{{ t('hosts.explain.wgTitle') }}</h3></div>
        <div class="cb"><p class="muted small">{{ t('hosts.explain.wg') }}</p></div>
      </section>
      <section class="card">
        <div class="ch"><span class="ico-tile acc"><Icon name="shield" :size="15" /></span><h3>{{ t('hosts.explain.ovpnTitle') }}</h3></div>
        <div class="cb"><p class="muted small">{{ t('hosts.explain.ovpn') }}</p></div>
      </section>
      <section class="card">
        <div class="ch"><span class="ico-tile acc"><Icon name="shield" :size="15" /></span><h3>{{ t('hosts.explain.otherTitle') }}</h3></div>
        <div class="cb"><p class="muted small">{{ t('hosts.explain.other') }}</p></div>
      </section>
    </div>

    <Teleport to="body">
      <div v-if="popover" class="apopover" :style="{ top: popover.y + 'px', left: popover.x + 'px', transform: 'translateX(-100%)' }" @click.stop>
        <div class="chips-stack"><span v-for="it in popover.items.slice(1)" :key="it" class="atag ltr" style="margin: 0">{{ it }}</span></div>
      </div>
    </Teleport>

    <HostForm v-if="formFor" :group="formFor.group" :interfaces="interfaces" @saved="onSaved" @cancel="formFor = null" />

    <ConfirmDialog
      :open="!!ask"
      :title="ask?.title || ''"
      :body="ask?.body || ''"
      :confirm-label="ask?.confirmLabel || ''"
      :danger="!!ask?.danger"
      :busy="busy"
      @confirm="runConfirmed"
      @cancel="ask = null"
    />
  </div>
</template>

<style scoped>
/* The "+N" popover is teleported out of the page, so it keeps the app's
   own popover and tag styles; only its stack layout lives here. */
.chips-stack { display: flex; flex-direction: column; gap: 4px; max-width: 280px; max-height: 280px; overflow-y: auto; }
</style>
