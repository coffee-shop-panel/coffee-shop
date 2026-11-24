<script setup>
// The overview's machine readings in the design's arrangement: four
// vital tiles across (a number read against its own recent history),
// then the throughput chart with a side stack — connections, uptime and
// the server's addresses (OverviewStrip) — beside it.
import { computed } from 'vue'
import { store, t } from '../../lib/store.js'
import { bytes } from '../../lib/format.js'
import Icon from '../Icon.vue'
import Sparkline from '../Sparkline.vue'
import OverviewStrip from './OverviewStrip.vue'

const props = defineProps({
  // The host's readings, with their recent history.
  system: { type: Object, required: true },
  panel: { type: Object, default: null },
})
const sys = computed(() => props.system)
const hist = computed(() => sys.value?.history || {})
const nf = (n) => Number(n || 0).toLocaleString(store.locale)
const rate = (bps) => `${bytes(bps, store.locale)}/s`

const stat = (arr) => {
  const a = (arr || []).filter(Number.isFinite)
  if (!a.length) return { min: 0, max: 0, mean: 0 }
  return {
    min: Math.min(...a),
    max: Math.max(...a),
    mean: a.reduce((x, y) => x + y, 0) / a.length,
  }
}

// Four vitals, in the classic panel's order. Each carries its own recent history so the
// number is read against where it has been, not on its own.
const vitals = computed(() => {
  const s = sys.value
  if (!s) return []
  const mk = (key, icon, label, usage, detail, series, empty = false) => {
    const st = stat(series)
    return {
      key,
      icon,
      label,
      percent: usage.percent,
      detail,
      range: `${t('overview.min')} ${st.min.toFixed(0)}% · ${t('overview.max')} ${st.max.toFixed(0)}%`,
      data: series || [],
      mean: st.mean,
      empty,
    }
  }
  return [
    mk('cpu', 'dashboard', 'CPU', s.cpu, `${nf(s.cpu.cores)} ${t('overview.cores')}`, hist.value.cpu),
    mk(
      'mem',
      'database',
      'RAM',
      s.memory,
      `${bytes(s.memory.used, store.locale)} / ${bytes(s.memory.total, store.locale)}`,
      hist.value.memory,
    ),
    mk(
      'swap',
      'swap',
      t('overview.swap'),
      s.swap,
      s.swap.total
        ? `${bytes(s.swap.used, store.locale)} / ${bytes(s.swap.total, store.locale)}`
        : t('overview.noSwap'),
      hist.value.swap,
      !s.swap.total,
    ),
    mk(
      'disk',
      'hdd',
      t('overview.storage'),
      s.disk,
      `${bytes(s.disk.used, store.locale)} / ${bytes(s.disk.total, store.locale)}`,
      hist.value.disk,
    ),
  ]
})

// The number stays neutral until it means something. On a red-accented panel an
// accent-coloured reading looks like an alarm at every level, which would leave
// nothing to say when one is warranted. The line below it keeps the brand
// colour, where red is decoration rather than a warning.
function vitalColor(p) {
  if (p >= 92) return 'var(--bad)'
  if (p >= 75) return 'var(--warn)'
  return 'var(--ink)'
}
function vitalLine(p) {
  if (p >= 92) return 'var(--bad)'
  if (p >= 75) return 'var(--warn)'
  return 'var(--accent)'
}

const upStat = computed(() => stat(hist.value.up))
const downStat = computed(() => stat(hist.value.down))
const totalConns = computed(
  () => (sys.value?.network.tcpConns || 0) + (sys.value?.network.udpConns || 0),
)
const connPct = (n) => (totalConns.value ? Math.round(((n || 0) / totalConns.value) * 100) : 0)
</script>

<template>
  <div class="grid g12 mb">
    <section v-for="v in vitals" :key="v.key" class="card s3">
      <div class="cb">
        <div class="row between">
          <div class="row tight">
            <span class="ico-tile"><Icon :name="v.icon" :size="15" /></span>
            <b>{{ v.label }}</b>
          </div>
          <span class="faint xs mono">{{ v.range }}</span>
        </div>
        <div class="row nowrap" style="margin-top: 10px; align-items: flex-end; gap: 14px">
          <div class="stat" style="padding: 0">
            <div class="val" :class="{ faint: v.empty }" :style="v.empty ? {} : { color: vitalColor(v.percent) }">
              {{ v.empty ? '—' : Math.round(v.percent) + '%' }}
            </div>
            <div class="foot">{{ v.detail }}</div>
          </div>
          <div style="flex: 1; min-width: 0">
            <!-- Left to scale itself: the big number already states the level, so
                 the line's job is the shape of the last few minutes. Pinned to
                 0–100 a steady 43% would draw a flat slab and say nothing. -->
            <Sparkline
              :series="[{ data: v.data, color: vitalLine(v.percent) }]"
              :reference="[{ value: v.mean }]"
              :height="44"
            />
          </div>
        </div>
      </div>
    </section>
  </div>

  <div class="grid g12 mb">
    <!-- Throughput -->
    <section class="card s8">
      <div class="ch">
        <h3>{{ t('overview.throughput') }}</h3>
        <span class="sub">{{ t('overview.throughputSub') }}</span>
      </div>
      <div class="cb">
        <Sparkline
          :series="[
            { data: hist.down, color: 'var(--sky)', fill: false },
            { data: hist.up, color: 'var(--accent)', fill: false },
          ]"
          :reference="[
            { value: sys.network.recvRate, color: 'var(--sky)' },
            { value: sys.network.sentRate, color: 'var(--accent)' },
          ]"
          :height="186"
          :min="0"
        />
        <div class="row between mt">
          <div class="legend">
            <span><i></i>{{ t('overview.upload') }} <b class="mono">{{ rate(sys.network.sentRate) }}</b></span>
            <span><i class="sky"></i>{{ t('overview.download') }} <b class="mono">{{ rate(sys.network.recvRate) }}</b></span>
          </div>
          <span class="faint small">
            {{ t('overview.sent') }} {{ bytes(sys.network.bytesSent, store.locale) }} ·
            {{ t('overview.received') }} {{ bytes(sys.network.bytesRecv, store.locale) }} ·
            {{ t('overview.peak') }} ↓ {{ rate(downStat.max) }} ↑ {{ rate(upStat.max) }}
          </span>
        </div>
      </div>
    </section>

    <div class="s4 grid" style="align-content: start">
      <!-- Connections -->
      <section class="card">
        <div class="ch">
          <h3>{{ t('overview.connections') }}</h3>
          <span class="faint small spacer mono">{{ nf(totalConns) }} {{ t('overview.open') }}</span>
        </div>
        <div class="cb tight stack">
          <div>
            <div class="row between small">
              <span>TCP</span>
              <b class="mono">{{ nf(sys.network.tcpConns) }}</b>
            </div>
            <div class="dmeter"><i :style="{ width: connPct(sys.network.tcpConns) + '%' }"></i></div>
          </div>
          <div>
            <div class="row between small">
              <span>UDP</span>
              <b class="mono">{{ nf(sys.network.udpConns) }}</b>
            </div>
            <div class="dmeter"><i class="sky" :style="{ width: connPct(sys.network.udpConns) + '%' }"></i></div>
          </div>
        </div>
      </section>

      <!-- Uptime, panel memory, and the server's addresses. -->
      <OverviewStrip :system="sys" :panel="panel" />
    </div>
  </div>
</template>
