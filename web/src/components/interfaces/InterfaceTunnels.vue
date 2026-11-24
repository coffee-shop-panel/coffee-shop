<script setup>
// The tunnels as the design's cards, one each: the protocol tile, the
// name and its facts, the customer counts, the traffic, and the row's
// own menu — the same actions the table's rows offered, in the shape
// the design draws them. The pager rides in the default slot, as it
// did under the table.
import { store, t } from '../../lib/store.js'
import { bytes } from '../../lib/format.js'
import { protoTag, rowItems, speedActive, transportTag } from '../../lib/interfaces/rows.js'
import AntIcon from '../AntIcon.vue'
import MoreMenu from '../MoreMenu.vue'
import Toggle from '../Toggle.vue'

defineProps({
  rows: { type: Array, required: true },
  selected: { type: Object, required: true },
  allSelected: { type: Boolean, default: false },
  // Rows with a change in flight, whose switch spins.
  pending: { type: Object, required: true },
})
const emit = defineEmits(['toggle', 'toggle-all', 'enable', 'pick', 'edit'])

const nf = (n) => Number(n || 0).toLocaleString(store.locale)
const rate = (v) => `${bytes(v || 0, store.locale)}/s`

// The protocol's full name where the app has one, its wire name where
// it does not — the same reading the table's protocol column gave.
const protocolLabel = (i) =>
  i.mode === 'amnezia'
    ? t('protocol.amneziawg')
    : ({ wireguard: t('protocol.wireguard'), openvpn: t('protocol.openvpn') })[i.protocol] || i.protocol

// The tile: a colour and a short mark per protocol family, the design's
// way of telling the tunnels apart at a glance.
function tileOf(i) {
  const p = i.protocol || ''
  if (i.xui) return { cls: 'x', label: 'X' }
  if (p === 'wireguard') return i.mode === 'amnezia' ? { cls: 'awg', label: 'AWG' } : { cls: 'wg', label: 'WG' }
  if (p === 'openvpn') return { cls: 'ovpn', label: 'OVPN' }
  if (p === 'l2tp') return { cls: 'l2tp', label: 'L2TP' }
  if (p === 'ikev2') return { cls: 'ikev2', label: 'IKEv2' }
  if (p === 'pptp') return { cls: 'pptp', label: 'PPTP' }
  if (p === 'openconnect') return { cls: 'oc', label: 'OC' }
  return { cls: 'off', label: (p || '?').slice(0, 4).toUpperCase() }
}
</script>

<template>
  <div class="stack" style="gap: 12px">
    <div class="row tight">
      <input
        type="checkbox"
        class="acheck"
        :checked="allSelected"
        :aria-label="t('action.selectAll')"
        @change="emit('toggle-all', $event.target.checked)"
      />
      <span class="faint small">{{ t('action.selectAll') }}</span>
    </div>

    <div v-if="!rows.length" class="dempty">{{ t('common.nothingYet') }}</div>

    <article v-for="i in rows" :key="i.id" class="card" :class="{ dim: !i.enabled }">
      <div class="tunnel">
        <input
          type="checkbox"
          class="acheck"
          :checked="selected.has(i.id)"
          :aria-label="i.name"
          @change="emit('toggle', i.id, $event.target.checked)"
        />
        <span class="proto" :class="[tileOf(i).cls, { long: tileOf(i).label.length >= 4 }]">{{ tileOf(i).label }}</span>
        <div class="col" style="min-width: 0">
          <div class="row tight">
            <h4>{{ i.name }}</h4>
            <span v-if="i.stealth?.transport" class="dtag sq acc">{{ i.stealth.transport }}</span>
            <span v-else-if="i.mode === 'amnezia'" class="dtag sq purple">AmneziaWG</span>
            <span v-if="i.xui" class="dtag sq acc">{{ protoTag(i) }}</span>
          </div>
          <span class="faint small mono">
            {{ protocolLabel(i) }} · {{ transportTag(i) }} · :{{ i.listenPort }} · {{ i.subnet }}
          </span>
          <div class="row tight">
            <Toggle
              :model-value="i.enabled"
              :label="i.name"
              small
              :disabled="!!i.xui"
              :loading="pending.has(i.id)"
              @update:model-value="(v) => emit('enable', i, v)"
            />
            <span class="small" :class="i.enabled && i.running ? 'ok-t' : 'faint'">
              {{ i.enabled && i.running ? t('overview.running') : t('iface.stopped') }}
            </span>
            <span class="faint small">·</span>
            <span v-if="i.nodeLocal" class="small">{{ t('iface.col.localPanel') }}</span>
            <span v-else class="small info-t">{{ i.nodeName }}</span>
          </div>
        </div>

        <div class="vsep">
          <div class="row" style="gap: 16px">
            <span class="cnt"><b>{{ nf(i.clients) }}</b><span>{{ t('nav.clients') }}</span></span>
            <span class="cnt ok"><b>{{ nf(i.active) }}</b><span>{{ t('status.active') }}</span></span>
            <span class="cnt"><b>{{ nf(i.disabled) }}</b><span>{{ t('status.disabled') }}</span></span>
            <span class="cnt bad"><b>{{ nf(i.depleted) }}</b><span>{{ t('stat.depleted') }}</span></span>
            <span class="cnt info"><b>{{ nf(i.online) }}</b><span>{{ t('status.online') }}</span></span>
          </div>
        </div>

        <div class="vsep traffic col" style="gap: 3px; justify-content: center">
          <span class="kicker">{{ t('client.traffic') }}</span>
          <b style="font-size: 18px">{{ bytes(i.usedBytes, store.locale) }} <span class="faint" style="font-size: 12px">/ ∞</span></b>
          <span v-if="speedActive(i)" class="mono xs">
            <span class="ok-t">↑{{ rate(i.speed.up) }}</span> · <span class="sky-t">↓{{ rate(i.speed.down) }}</span>
          </span>
          <span v-else class="faint xs">—</span>
        </div>

        <div class="acts">
          <button class="act" :title="t('action.edit')" :aria-label="t('action.edit')" @click="emit('edit', i)">
            <AntIcon name="EditOutlined" />
          </button>
          <MoreMenu :items="rowItems(i)" compact button-class="act" @pick="(key) => emit('pick', i, key)" />
        </div>
      </div>
    </article>

    <slot></slot>
  </div>
</template>
