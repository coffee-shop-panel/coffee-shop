<script setup>
// The outbounds as a table: their columns, in their order -- #, Tag,
// Address, Egress (with the eye that hides the address), Country,
// Traffic, Latency, Check.
import { ref } from 'vue'
import { store, t } from '../../lib/store.js'
import { bytes } from '../../lib/format.js'
import { checkedAgo, countryFlag, countryName, menuFor, modeLabel } from '../../lib/outbounds/rows.js'
import Icon from '../Icon.vue'
import AntIcon from '../AntIcon.vue'
import MoreMenu from '../MoreMenu.vue'

defineProps({
  outbounds: { type: Array, required: true },
  pending: { type: Set, required: true },
  // The test mode, named on each row's Check button.
  mode: { type: String, required: true },
})
// pick(o, idx, key): a row menu choice.
defineEmits(['edit', 'pick', 'check'])

const showEgressIp = ref(false)
const kindLabel = (o) => t(`outbound.kind.${o.kind}`)
</script>

<template>
  <div class="tw">
    <table>
      <thead>
        <tr>
          <th class="w0">#</th>
          <th>{{ t('outbound.tag') }}</th>
          <th>{{ t('outbound.address') }}</th>
          <th>
            <span class="egress-header">
              {{ t('outbound.egress') }}
              <button
                type="button"
                class="ip-toggle"
                :aria-label="t('outbound.toggleIpVisibility')"
                :title="t('outbound.toggleIpVisibility')"
                @click="showEgressIp = !showEgressIp"
              >
                <AntIcon :name="showEgressIp ? 'EyeOutlined' : 'EyeInvisibleOutlined'" />
              </button>
            </span>
          </th>
          <th>{{ t('outbound.country') }}</th>
          <th>{{ t('outbound.traffic') }}</th>
          <th>{{ t('outbound.latency') }}</th>
          <th class="w0">{{ t('outbound.check') }}</th>
        </tr>
      </thead>
      <tbody>
        <tr v-if="!outbounds.length">
          <td colspan="8">
            <div class="dempty">
              <span class="ico-tile"><Icon name="upload" :size="22" /></span>
              <div>{{ t('common.nothingYet') }}</div>
            </div>
          </td>
        </tr>
        <tr v-for="(o, idx) in outbounds" :key="o.id" :class="{ off: !o.enabled }">
          <!-- The design's step chip, lit on the first row, with the row's
               two circles beside it: edit, and the menu with the rest. -->
          <td class="w0 keep">
            <div class="row nowrap tight">
              <span class="step" :class="{ first: idx === 0 }">{{ idx + 1 }}</span>
              <button
                class="act"
                :aria-label="t('action.edit')"
                :title="t('action.edit')"
                :disabled="pending.has(o.id)"
                @click="$emit('edit', o)"
              >
                <Icon name="edit" :size="15" />
              </button>
              <MoreMenu :items="menuFor(o, idx, outbounds.length)" compact button-class="act" :label="t('action.more')" @pick="(key) => $emit('pick', o, idx, key)" />
            </div>
          </td>

          <td>
            <b class="mono" :title="o.tag">{{ o.tag }}</b>
            <div class="note">{{ kindLabel(o) }}<template v-if="o.note"> · {{ o.note }}</template></div>
          </td>

          <td>
            <span v-if="o.address" class="mono small" dir="ltr" :title="o.address">{{ o.address }}</span>
            <span v-else class="muted">—</span>
          </td>

          <td>
            <span v-if="o.egressIp" class="mono small blur" :class="{ shown: showEgressIp }" dir="ltr" :title="showEgressIp ? o.egressIp : ''">{{ o.egressIp }}</span>
            <span v-else class="muted" :title="t('outbound.egressHint')">—</span>
          </td>

          <td>
            <span v-if="o.egressCountry" title="Cloudflare trace">
              <span>{{ countryFlag(o.egressCountry) }}</span>
              <span>{{ countryName(o.egressCountry, store.locale) || o.egressCountry }}</span>
            </span>
            <span v-else class="muted" :title="t('outbound.egressHint')">—</span>
          </td>

          <td class="mono xs" dir="ltr">
            <span class="ok-t">↑ {{ bytes(o.txBytes || 0) }}</span>
            <span class="sky-t">↓ {{ bytes(o.rxBytes || 0) }}</span>
          </td>

          <td>
            <AntIcon v-if="pending.has(o.id)" name="LoadingOutlined" class="anticon-spin" />
            <template v-else-if="o.lastError">
              <span class="dtag bad" :title="o.lastError">{{ t('outbound.failed') }}</span>
            </template>
            <template v-else-if="o.latencyMs">
              <span class="ok-t mono">{{ o.latencyMs }} ms</span>
              <div class="note">{{ checkedAgo(o) }}</div>
            </template>
            <span v-else class="faint">—</span>
          </td>

          <td class="w0 keep">
            <button
              class="btn xs"
              :aria-label="t('outbound.check')"
              :title="`${t('outbound.check')} (${modeLabel(mode)})`"
              :disabled="pending.has(o.id) || o.kind === 'block'"
              @click="$emit('check', o)"
            >
              <span v-if="pending.has(o.id)" class="spin sm"></span>
              <Icon v-else name="zap" :size="13" />
              {{ t('outbound.check') }}
            </button>
          </td>
        </tr>
      </tbody>
    </table>
  </div>
</template>

<style scoped>
/* The egress header carries its eye inline; the row's first cell keeps
   the design's nowrap row of chip and circles. */
.egress-header { display: inline-flex; align-items: center; gap: 6px; }
.ip-toggle { display: inline-grid; place-items: center; border: 0; background: none; color: var(--faint); cursor: pointer; padding: 2px; }
.ip-toggle:hover { color: var(--ink); }
.row.nowrap { display: flex; align-items: center; flex-wrap: nowrap; }
</style>
