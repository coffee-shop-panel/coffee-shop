<script setup>
// The customer list as a table, a row each, for a screen wide enough —
// in the design's table treatment, with the row's details folded into
// an expansion under it: the devices, the facts the columns leave out,
// and the ways in that the row's own buttons do not offer.
import { ref } from 'vue'
import { store, t } from '../../lib/store.js'
import { bytes, dateTime, relative } from '../../lib/format.js'
import {
  INBOUND_CHIP_LIMIT, expiryTag, expiryTitle, inboundChips, lastOnlineTitle, ownerTags, remainingTag, speedActive,
  statusTag, usedPercent,
} from '../../lib/clientRow.js'
import AntIcon from '../AntIcon.vue'
import Toggle from '../Toggle.vue'

defineProps({
  // The customers on this page, and which are chosen.
  items: { type: Array, required: true },
  selected: { type: Object, required: true },
  allSelected: { type: Boolean, default: false },
  // Rows with a change in flight, whose switch spins.
  pending: { type: Object, required: true },
  interfaces: { type: Array, default: () => [] },
  // The operators above each customer, by id, for the owner's ownership
  // tags; null for anyone the server does not hand other operators' ids.
  operators: { type: Object, default: null },
  // Whether any customer has a group, which adds that column.
  hasGroups: { type: Boolean, default: false },
  groupFilter: { type: String, default: '' },
  stale: { type: Boolean, default: false },
})
const emit = defineEmits(['toggle', 'toggle-all', 'qr', 'info', 'rotate', 'reset', 'edit', 'remove', 'enable', 'group', 'extend'])

const nf = (n) => Number(n || 0).toLocaleString(store.locale)
const rate = (v) => `${bytes(v || 0, store.locale)}/s`

// Which rows are open. A row's expansion is read from the row itself —
// the list's rows already carry their devices — so opening one costs no
// request and survives the quiet polls that patch rows in place.
const expanded = ref(new Set())
function toggleExpanded(id) {
  const next = new Set(expanded.value)
  next.has(id) ? next.delete(id) : next.add(id)
  expanded.value = next
}

// The traffic meter's tone, the design's way: grey when the customer is
// off, purple when there is no ceiling to fill, then the warning bands.
function meterTone(c) {
  if (c.status === 'disabled') return 'gray'
  if (!c.quotaBytes) return 'purple'
  const p = usedPercent(c) ?? 0
  if (p >= 100) return 'bad'
  if (p >= 80) return 'warn'
  return 'ok'
}

// The remaining and duration cells read as coloured figures, not tags:
// green what is left, orange what is nearly gone, red what is gone,
// purple what has no end.
const toneText = { green: 'ok-t', orange: 'warn-t', red: 'bad-t', purple: 'purple-t', blue: 'info-t', grey: '' }

// A telegram id is a customer's own number; the row shows enough of it
// to recognise and not enough to copy, the way the design masks it.
function maskId(id) {
  const s = String(id || '')
  if (s.length <= 6) return s
  return `${s.slice(0, 4)}•••${s.slice(-2)}`
}

function lastSeen(c) {
  let last = null
  for (const a of c.accounts || []) {
    if (a.lastHandshake && (!last || a.lastHandshake > last)) last = a.lastHandshake
  }
  return last
}
</script>

<template>
  <div class="atable-wrap tw" :class="{ stale }" style="margin-top: 0">
    <table class="atable small" style="min-width: 1200px">
      <thead>
        <tr>
          <th class="sel"><input type="checkbox" class="acheck" :checked="allSelected" :aria-label="t('action.selectAll')" @change="emit('toggle-all', $event.target.checked)" /></th>
          <th style="width: 236px">{{ t('table.actions') }}</th>
          <th style="width: 80px">{{ t('table.enabled') }}</th>
          <th style="width: 90px">{{ t('status.online') }}</th>
          <th style="width: 220px">{{ t('client.menu.client') }}</th>
          <th v-if="hasGroups" style="width: 130px">{{ t('client.groups') }}</th>
          <th style="width: 170px">{{ t('client.attachedInbounds') }}</th>
          <th style="width: 300px">{{ t('client.traffic') }}</th>
          <th class="center" style="width: 216px">{{ t('client.speed') }}</th>
          <th style="width: 130px">{{ t('client.remaining') }}</th>
          <th style="width: 130px">{{ t('client.menu.duration') }}</th>
        </tr>
      </thead>
      <tbody>
        <tr v-if="!items.length" class="empty-row">
          <td :colspan="hasGroups ? 11 : 10">
            <div class="clients-empty">
              <AntIcon name="TeamOutlined" :size="32" />
              <div>{{ t('common.nothingYet') }}</div>
            </div>
          </td>
        </tr>
        <template v-for="c in items" :key="c.id">
          <tr :class="{ picked: selected.has(c.id), off: c.status === 'disabled' }">
            <td class="sel keep"><input type="checkbox" class="acheck" :checked="selected.has(c.id)" :aria-label="c.name" @change="emit('toggle', c.id, $event.target.checked)" /></td>
            <td class="keep">
              <div class="acts">
                <button class="act" :title="t('client.qrCode')" :aria-label="t('client.qrCode')" @click="emit('qr', c)"><AntIcon name="QrcodeOutlined" /></button>
                <button class="act" :title="t('client.menu.clientInfo')" :aria-label="t('client.menu.clientInfo')" @click="emit('info', c)"><AntIcon name="InfoCircleOutlined" /></button>
                <button class="act" :title="t('client.rotateKeys')" :aria-label="t('client.rotateKeys')" :disabled="pending.has(c.id)" @click="emit('rotate', c)"><AntIcon name="KeyOutlined" /></button>
                <button class="act" :title="t('outbound.resetTraffic')" :aria-label="t('outbound.resetTraffic')" :disabled="pending.has(c.id)" @click="emit('reset', c)"><AntIcon name="RetweetOutlined" /></button>
                <button class="act" :title="t('action.edit')" :aria-label="t('action.edit')" @click="emit('edit', c)"><AntIcon name="EditOutlined" /></button>
                <button class="act danger" :title="t('action.delete')" :aria-label="t('action.delete')" @click="emit('remove', c)"><AntIcon name="DeleteOutlined" /></button>
                <button class="act" :title="t('client.openPage')" :aria-label="t('client.openPage')" :aria-expanded="expanded.has(c.id)" @click="toggleExpanded(c.id)">
                  <AntIcon :name="expanded.has(c.id) ? 'DownOutlined' : 'RightOutlined'" />
                </button>
              </div>
            </td>
            <td class="keep">
              <span :title="c.ownerPaused ? statusTag(c).title : ''" class="toggle-wrap"><Toggle :model-value="c.status === 'active' && !c.ownerPaused" :label="c.name" small :disabled="c.status === 'expired' || c.status === 'exhausted' || !!c.ownerPaused" :loading="pending.has(c.id)" @update:model-value="(v) => emit('enable', c, v)" /></span>
            </td>
            <td>
              <span class="atag" :class="statusTag(c).color === 'grey' ? '' : statusTag(c).color" :title="statusTag(c).title || lastOnlineTitle(c)" style="margin: 0">
                <i v-if="statusTag(c).dot" class="online-dot"></i>{{ statusTag(c).label }}
              </span>
            </td>
            <td>
              <div class="email-cell">
                <!-- The name opens the customer's own page: their files one
                     by one, each with its configuration and QR, and files
                     added or removed beyond the one per user. -->
                <router-link class="email name" :to="`/clients/${c.id}`" :title="t('client.openPage')">{{ c.name }}</router-link>
                <!-- Connections in use right now against what the plan allows at once,
                     the way the classic panel shows its IP count: a file on two devices
                     used one at a time is one connection; both at once is two. -->
                <span class="sub ltr" :class="{ over: c.deviceLimit > 0 && (c.onlineNow || 0) > c.deviceLimit }"
                      :title="t('client.connectionsNow', { n: c.onlineNow || 0, limit: c.deviceLimit, files: c.accounts?.length ?? 0 })">
                  {{ c.onlineNow || 0 }} / {{ c.deviceLimit || '∞' }}
                </span>
                <span v-if="c.note" class="note" :title="c.note">{{ c.note }}</span>
                <!-- The owner's traceability: the admin at the top of this
                     customer's chain, and the reseller who sold them. -->
                <span v-for="tag in ownerTags(c, operators)" :key="tag.kind"
                      class="atag" :class="tag.kind === 'admin' ? 'purple' : 'geekblue'"
                      style="margin: 2px 2px 0 0" :title="`${tag.label}: ${tag.name}`">{{ tag.label }}: {{ tag.name }}</span>
              </div>
            </td>
            <td v-if="hasGroups">
              <!-- As the inbounds cell: the first group as a chip, the rest
                   folded into a +N that names them on hover, so a customer
                   in five groups is still one row high. -->
              <template v-if="(c.groups || []).length">
                <span v-for="g in c.groups.slice(0, INBOUND_CHIP_LIMIT)" :key="g" class="atag geekblue" :style="{ margin: '2px', cursor: 'pointer', opacity: groupFilter === g ? 0.6 : 1 }" :title="g" @click="emit('group', g)">{{ g }}</span>
                <span v-if="c.groups.length > INBOUND_CHIP_LIMIT" class="atag default" style="margin: 2px; cursor: pointer" :title="c.groups.slice(INBOUND_CHIP_LIMIT).join(', ')" @click="emit('group', c.groups[INBOUND_CHIP_LIMIT])">+{{ c.groups.length - INBOUND_CHIP_LIMIT }}</span>
              </template>
              <span v-else class="cell-empty">—</span>
            </td>
            <td>
              <template v-if="inboundChips(c, interfaces).length">
                <span v-for="ib in inboundChips(c, interfaces).slice(0, INBOUND_CHIP_LIMIT)" :key="ib.name" class="atag sq" :class="{ openvpn: 'orange', l2tp: 'purple' }[ib.protocol] || 'gold'" style="margin: 2px" :title="ib.name">{{ ib.name }}</span>
                <span v-if="inboundChips(c, interfaces).length > INBOUND_CHIP_LIMIT" class="atag default" style="margin: 2px; cursor: pointer" :title="inboundChips(c, interfaces).slice(INBOUND_CHIP_LIMIT).map((x) => x.name).join(', ')">+{{ inboundChips(c, interfaces).length - INBOUND_CHIP_LIMIT }}</span>
              </template>
              <span v-else class="cell-empty">—</span>
            </td>
            <td>
              <div class="usage" :title="`↑ ${bytes(c.upBytes || 0, store.locale)}  ↓ ${bytes(c.downBytes || 0, store.locale)}`">
                <div class="top">
                  <b>{{ bytes(c.usedBytes, store.locale) }}</b>
                  <span>/ {{ c.quotaBytes ? bytes(c.quotaBytes, store.locale) : '∞' }}</span>
                </div>
                <div class="dmeter">
                  <i :class="meterTone(c)" :style="{ width: (c.quotaBytes ? Math.min(100, usedPercent(c) ?? 0) : 100) + '%' }"></i>
                </div>
              </div>
            </td>
            <td class="center">
              <span v-if="speedActive(c)" class="mono xs" style="white-space: nowrap">
                <span class="ok-t">↑{{ rate(c.speed.up) }}</span> <span class="sky-t">↓{{ rate(c.speed.down) }}</span>
              </span>
              <span v-else class="cell-empty">—</span>
            </td>
            <td><span class="num" :class="toneText[remainingTag(c).color]" style="font-weight: 650">{{ remainingTag(c).label }}</span></td>
            <td><span class="num" :class="toneText[expiryTag(c).color]" :title="expiryTitle(c)" style="font-weight: 650">{{ expiryTag(c).label }}</span></td>
          </tr>

          <!-- The row opened: the devices one by one, the facts the
               columns leave out, and the slower ways in. -->
          <tr v-if="expanded.has(c.id)" class="exp-row">
            <td :colspan="hasGroups ? 11 : 10">
              <div class="exp">
                <div>
                  <div class="kicker" style="margin-bottom: 6px">
                    {{ t('device.title') }} · {{ nf(new Set((c.accounts || []).map((a) => a.deviceName.toLowerCase())).size) }} / {{ c.deviceLimit || '∞' }}
                  </div>
                  <ul class="list feed">
                    <li v-for="a in c.accounts || []" :key="a.id" style="padding: 7px 0">
                      <span class="mono small">{{ a.deviceName }}</span>
                      <span class="faint xs mono">{{ a.ip }}</span>
                      <span class="when">{{ a.lastHandshake ? relative(a.lastHandshake, store.locale) : '—' }}</span>
                    </li>
                    <li v-if="!(c.accounts || []).length" style="padding: 7px 0">
                      <span class="faint small">{{ t('device.none') }}</span>
                    </li>
                  </ul>
                </div>
                <dl class="kv left">
                  <dt>{{ t('nav.interfaces') }}</dt>
                  <dd>
                    <span v-for="ib in inboundChips(c, interfaces)" :key="ib.name" class="atag sq gold" style="margin: 2px">{{ ib.name }}</span>
                    <span v-if="!inboundChips(c, interfaces).length">—</span>
                  </dd>
                  <dt>{{ t('client.resetCycle') }}</dt>
                  <dd>{{ t(`reset.${c.resetCycle}`) }}</dd>
                  <dt v-if="c.telegramId">{{ t('client.telegramId') }}</dt>
                  <dd v-if="c.telegramId" class="mono">{{ maskId(c.telegramId) }}</dd>
                </dl>
                <dl class="kv left">
                  <dt>{{ t('client.subscription') }}</dt>
                  <dd class="mono xs">{{ c.subId ? `/subscribe/${c.subId}` : '—' }}</dd>
                  <dt>{{ t('iface.col.createdAt') }}</dt>
                  <dd>{{ c.createdAt ? dateTime(c.createdAt, store.locale) : '—' }}</dd>
                  <dt>{{ t('client.menu.lastOnline') }}</dt>
                  <dd>{{ lastSeen(c) ? relative(lastSeen(c), store.locale) : '—' }}</dd>
                  <dt>{{ t('overview.upload') }} / {{ t('overview.download') }}</dt>
                  <dd class="mono xs">↑ {{ bytes(c.upBytes || 0, store.locale) }} · ↓ {{ bytes(c.downBytes || 0, store.locale) }}</dd>
                </dl>
                <div class="col" style="gap: 6px">
                  <button class="btn sm" @click="emit('edit', c)">{{ t('action.edit') }}</button>
                  <button class="btn sm" @click="emit('extend', c, 'time')">{{ t('overview.addTime') }}</button>
                  <button class="btn sm" @click="emit('extend', c, 'traffic')">{{ t('overview.addTraffic') }}</button>
                  <router-link class="btn sm" :to="`/clients/${c.id}`">{{ t('overview.openPage') }}</router-link>
                </div>
              </div>
            </td>
          </tr>
        </template>
      </tbody>
    </table>
  </div>
</template>

<style scoped>
.atable-wrap.stale { opacity: 0.6; }
.email-cell { display: flex; flex-direction: column; }
.email-cell .email { font-weight: 500; color: inherit; text-decoration: none; }
.email-cell .sub.over { color: var(--bad); font-weight: 600; }
.email-cell .sub { font-size: 11px; opacity: 0.55; font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; max-width: 220px; }
.email-cell .note { white-space: nowrap; overflow: hidden; text-overflow: ellipsis; max-width: 220px; }
.cell-empty { color: var(--faint); }
.clients-empty { padding: 32px 0; text-align: center; color: var(--muted); }
/* The design's table has no column separators in its head. */
.cs .tw .atable thead th::before { display: none; }
</style>
