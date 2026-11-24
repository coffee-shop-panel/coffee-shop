<script setup>
// The nodes as the design's cards, one each, on every screen wide or
// narrow: what the table's row said — state, load, transfer, version,
// heartbeat — and the same five things that can be done to a node,
// said in the card's own shape. The panel's own row keeps its
// restrictions: it cannot be switched off, edited or removed here.
import { store, t } from '../../lib/store.js'
import { bytes } from '../../lib/format.js'
import { ago, takesImport, uptime } from '../../lib/nodes/rows.js'
import AntIcon from '../AntIcon.vue'
import Icon from '../Icon.vue'
import Toggle from '../Toggle.vue'

defineProps({
  nodes: { type: Array, required: true },
  pending: { type: Set, required: true },
})
defineEmits(['probe', 'update', 'import', 'edit', 'remove', 'toggle'])

const kindLabel = (n) =>
  n.kind === 'local'
    ? t('node.thisPanel')
    : n.kind === 'xui'
      ? '3x-ui panel'
      : n.kind === 'pasarguard'
        ? 'Pasarguard panel'
        : t('node.kindWui')

const tlsLabel = (n) =>
  ({ verify: t('node.tlsVerify'), pin: t('node.tlsPin'), skip: t('node.tlsSkip'), mtls: t('node.tlsMutual') })[n.tlsMode] || ''

const stateOf = (n) =>
  n.kind === 'local'
    ? { tone: 'ok', label: t('node.running') }
    : !n.enabled
      ? { tone: 'gray', label: t('status.disabled') }
      : n.reachable
        ? { tone: 'ok', label: t('node.online') }
        : { tone: 'bad', label: t('node.offline') }

const tileTone = (n) => (n.kind === 'local' ? '' : !n.enabled ? '' : n.reachable ? 'ok' : 'bad')
const offline = (n) => n.kind !== 'local' && n.enabled && !n.reachable
const transferPct = (n) => (n.dataLimitBytes ? Math.min(100, ((n.usedBytes || 0) / n.dataLimitBytes) * 100) : 0)
</script>

<template>
  <div v-if="!nodes.length" class="dempty">{{ t('common.nothingYet') }}</div>
  <div v-else class="grid g4 node-grid">
    <article v-for="n in nodes" :key="n.id" class="card" :class="{ danger: offline(n) }">
      <div class="ch">
        <span class="ico-tile" :class="tileTone(n)"><Icon name="server" :size="17" /></span>
        <div class="col" style="flex: 1; min-width: 0">
          <b style="white-space: nowrap; overflow: hidden; text-overflow: ellipsis">{{ n.name }}</b>
          <span class="faint xs" style="white-space: nowrap; overflow: hidden; text-overflow: ellipsis">{{ kindLabel(n) }}<template v-if="n.kind !== 'local' && tlsLabel(n)"> · {{ tlsLabel(n) }}</template></span>
        </div>
        <span style="flex: none">
          <Toggle
            :model-value="n.enabled"
            :label="n.name"
            small
            :disabled="n.kind === 'local' || pending.has(n.id)"
            @update:model-value="(v) => $emit('toggle', n, v)"
          />
        </span>
      </div>

      <div class="cb stack" style="gap: 12px">
        <div class="row tight">
          <span class="dtag" :class="stateOf(n).tone" :title="offline(n) ? n.lastError || '' : ''">
            <span v-if="stateOf(n).tone === 'ok'" class="d"></span>{{ stateOf(n).label }}
          </span>
          <span v-if="n.overAllowance" class="dtag bad" :title="t('node.spentHint')">{{ t('node.spent') }}</span>
          <span
            v-if="n.usageCoefficient && n.usageCoefficient !== 1"
            class="dtag gray num ltr"
            :title="t('node.coefficientHint')"
          >x{{ n.usageCoefficient }}</span>
          <span v-if="n.kind !== 'local' && n.tlsMode === 'skip'" class="dtag bad" :title="t('node.tlsSkipWarning')">{{ t('node.tlsUnchecked') }}</span>
          <span v-else-if="n.kind !== 'local' && n.tlsMode === 'mtls'" class="dtag ok" :title="t('node.tlsMutualHint')">{{ t('node.tlsMutualShort') }}</span>
          <span v-else-if="n.kind !== 'local' && n.tlsMode === 'pin'" class="dtag gray" :title="t('node.tlsPinnedHint')">{{ t('node.tlsPinned') }}</span>
        </div>

        <span class="faint xs mono ltr" style="white-space: nowrap; overflow: hidden; text-overflow: ellipsis">{{ n.address || '—' }}</span>
        <p v-if="n.note" class="faint small" style="margin: 0">{{ n.note }}</p>

        <div class="grid g2" style="gap: 10px">
          <div>
            <div class="row between xs" style="margin-bottom: 5px">
              <span class="faint">{{ t('node.cpu') }}</span>
              <b class="mono">{{ n.cpuPercent ? n.cpuPercent.toFixed(0) + '%' : '—' }}</b>
            </div>
            <div class="dmeter"><i :style="{ width: (n.cpuPercent || 0) + '%' }"></i></div>
          </div>
          <div>
            <div class="row between xs" style="margin-bottom: 5px">
              <span class="faint">{{ t('node.mem') }}</span>
              <b class="mono">{{ n.memPercent ? n.memPercent.toFixed(0) + '%' : '—' }}</b>
            </div>
            <div class="dmeter"><i class="sky" :style="{ width: (n.memPercent || 0) + '%' }"></i></div>
          </div>
        </div>

        <div v-if="n.dataLimitBytes">
          <div class="row between xs" style="margin-bottom: 5px">
            <span class="faint">{{ t('node.transferPeriod') }}</span>
            <b class="mono">{{ bytes(n.usedBytes || 0, store.locale) }} / {{ bytes(n.dataLimitBytes, store.locale) }}</b>
          </div>
          <div class="dmeter">
            <i :class="n.overAllowance ? 'bad' : transferPct(n) >= 90 ? 'warn' : 'ok'" :style="{ width: transferPct(n) + '%' }"></i>
          </div>
        </div>

        <dl class="kv">
          <dt>{{ t('node.version') }}</dt>
          <dd class="mono">{{ n.version || '—' }}</dd>
          <dt>{{ t('node.uptime') }}</dt>
          <dd class="mono">{{ uptime(n.uptimeSec) }}</dd>
          <dt>{{ t('node.latency') }}</dt>
          <dd class="mono">{{ n.kind === 'local' ? '—' : (n.latencyMs || 0) + ' ms' }}</dd>
          <dt>{{ t('node.lastSeen') }}</dt>
          <dd>{{ n.kind === 'local' ? t('node.justNow') : ago(n.lastSeenAt) }}</dd>
        </dl>
      </div>

      <div class="cf">
        <div class="acts">
          <button class="act" :title="t('node.probe')" :disabled="n.kind === 'local' || pending.has(n.id)" @click="$emit('probe', n)">
            <span v-if="pending.has(n.id)" class="spin sm"></span>
            <Icon v-else name="refresh" :size="15" />
          </button>
          <button class="act" :title="t('update.askNode')" :disabled="n.kind === 'local' || pending.has(n.id)" @click="$emit('update', n)">
            <Icon name="download" :size="15" />
          </button>
          <button v-if="takesImport(n)" class="act" :title="t('nodeImport.action')" @click="$emit('import', n)">
            <AntIcon name="ImportOutlined" />
          </button>
          <button class="act" :title="t('action.edit')" :disabled="n.kind === 'local'" @click="$emit('edit', n)">
            <Icon name="edit" :size="15" />
          </button>
          <button class="act danger" :title="t('action.delete')" :disabled="n.kind === 'local'" @click="$emit('remove', n)">
            <Icon name="trash" :size="15" />
          </button>
        </div>
        <RouterLink class="btn xs ghost spacer" to="/interfaces">
          {{ t('nav.interfaces') }} {{ store.locale === 'fa' ? '←' : '→' }}
        </RouterLink>
      </div>
    </article>
  </div>
</template>

<style scoped>
/* The fleet in four columns on a wide screen, two on a tablet, one on
   a phone — the design's grid, stepped down with the panel. */
@media (max-width: 1100px) {
  .cs .grid.node-grid { grid-template-columns: repeat(2, 1fr); }
}
@media (max-width: 640px) {
  .cs .grid.node-grid { grid-template-columns: 1fr; }
}
</style>
