<script setup>
// The resellers as a table: a box to select each, its actions and switch,
// who they are, where they stand, and what is left of what they bought.
import { store, t } from '../../lib/store.js'
import { bytes } from '../../lib/format.js'
import { CHIP_LIMIT, barColor, customersTag, standing, termTag, trafficLeftTag, usedPercent } from '../../lib/resellers/rows.js'
import Icon from '../Icon.vue'
import Toggle from '../Toggle.vue'

const props = defineProps({
  rows: { type: Array, required: true },
  selected: { type: Set, required: true },
  pending: { type: Set, required: true },
  servers: { type: Array, required: true },
  allSelected: { type: Boolean, default: false },
  someSelected: { type: Boolean, default: false },
})
defineEmits(['toggle', 'toggle-all', 'edit', 'reset', 'remove', 'enable'])

const serverName = (id) => props.servers.find((s) => s.id === id)?.name || `#${id}`
const serverProto = (id) => props.servers.find((s) => s.id === id)?.protocol

// The row helpers name their colours in the app's tag vocabulary; the
// design's tags say the same things with other words.
const TONE = { green: 'ok', orange: 'warn', red: 'bad', blue: 'info', purple: 'purple', geekblue: 'info' }
const tone = (c) => TONE[c] || 'gray'
</script>

<template>
  <div class="tw">
    <table style="min-width: 1080px">
      <thead>
        <tr>
          <th class="w0"><input type="checkbox" :checked="allSelected" :indeterminate="someSelected" :aria-label="t('action.selectAll')" @change="$emit('toggle-all', $event.target.checked)" /></th>
          <th>{{ t('table.actions') }}</th>
          <th>{{ t('table.enabled') }}</th>
          <th>{{ t('admins.reseller') }}</th>
          <th>{{ t('admins.status') }}</th>
          <th>{{ t('admins.customers') }}</th>
          <th>{{ t('client.traffic') }}</th>
          <th>{{ t('admins.trafficLeft') }}</th>
          <th>{{ t('admins.timeLeft') }}</th>
          <th>{{ t('admins.servers') }}</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="a in rows" :key="a.id" :class="{ off: !a.enabled, picked: selected.has(a.id) }">
          <td class="w0 keep"><input type="checkbox" :checked="selected.has(a.id)" :aria-label="a.username" @change="$emit('toggle', a.id, $event.target.checked)" /></td>
          <td class="w0 keep">
            <div class="acts">
              <button class="act" :title="t('action.edit')" :aria-label="t('action.edit')" @click="$emit('edit', a)"><Icon name="edit" :size="16" /></button>
              <button v-if="a.role === 'reseller' && a.quotaBytes" class="act" :title="t('admins.resetUsage')" :aria-label="t('admins.resetUsage')" :disabled="pending.has(a.id)" @click="$emit('reset', a)"><Icon name="refresh" :size="16" /></button>
              <button class="act danger" :title="t('action.delete')" :aria-label="t('action.delete')" @click="$emit('remove', a)"><Icon name="trash" :size="16" /></button>
            </div>
          </td>
          <td class="keep">
            <Toggle :model-value="a.enabled" :label="a.username" small :loading="pending.has(a.id)" @update:model-value="(v) => $emit('enable', a, v)" />
          </td>
          <td>
            <b dir="ltr">{{ a.username }}</b>
            <span v-if="a.role === 'admin'" class="dtag info" style="margin-inline-start: 6px">{{ t('admins.role.admin') }}</span>
            <div v-if="a.note" class="note" :title="a.note">{{ a.note }}</div>
            <div v-if="a.groupName" class="note" :title="t('admins.groupHint')"><Icon name="tag" :size="11" /> {{ a.groupName }}</div>
          </td>
          <td><span class="dtag" dir="auto" :class="tone(standing(a).color)">{{ standing(a).label }}</span></td>
          <template v-if="a.role === 'reseller'">
            <td>
              <div class="usage">
                <div class="top"><b>{{ customersTag(a).label }}</b></div>
                <div v-if="a.clientLimit" class="dmeter"><i :class="customersTag(a).color === 'orange' ? 'warn' : 'ok'" :style="{ width: Math.min(100, Math.round((a.users / a.clientLimit) * 100)) + '%' }"></i></div>
              </div>
            </td>
            <td>
              <div class="usage">
                <div class="top ltr">
                  <b>{{ bytes(a.usedBytes, store.locale) }}</b>
                  <span>/ {{ a.quotaBytes ? bytes(a.quotaBytes, store.locale) : '∞' }}</span>
                </div>
                <div class="dmeter"><i :style="{ width: (a.quotaBytes ? usedPercent(a) : 100) + '%', background: barColor(a), boxShadow: 'none' }"></i></div>
              </div>
            </td>
            <td><span class="dtag" dir="ltr" :class="tone(trafficLeftTag(a).color)">{{ trafficLeftTag(a).label }}</span></td>
            <td><span class="dtag" dir="auto" :class="tone(termTag(a).color)" :title="termTag(a).title">{{ termTag(a).label }}</span></td>
            <td>
              <template v-if="(a.interfaceIds || []).length">
                <span v-for="id in a.interfaceIds.slice(0, CHIP_LIMIT)" :key="id" class="dtag sq" :class="serverProto(id) === 'openvpn' ? 'warn' : 'gold'" style="margin: 2px">{{ serverName(id) }}</span>
                <span v-if="a.interfaceIds.length > CHIP_LIMIT" class="dtag sq" style="margin: 2px" :title="a.interfaceIds.slice(CHIP_LIMIT).map(serverName).join(', ')">+{{ a.interfaceIds.length - CHIP_LIMIT }}</span>
              </template>
              <span v-else class="dtag bad" :title="t('admins.noServersWarn')">{{ t('admins.noServers') }}</span>
            </td>
          </template>
          <template v-else>
            <td colspan="5" class="faint small">{{ t('admins.role.adminHint') }}</td>
          </template>
        </tr>
      </tbody>
    </table>
  </div>
</template>
