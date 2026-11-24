<script setup>
// The strip over the tunnel list, in the design's stat cards: what the
// whole set has carried each way, in all, what it is moving right now,
// and how many tunnels there are and how many of them are running.
import { computed } from 'vue'
import { store, t } from '../../lib/store.js'
import { bytes } from '../../lib/format.js'

const props = defineProps({
  totals: { type: Object, required: true },
  // The tunnels on this tab, for the running count in the last card.
  rows: { type: Array, default: () => [] },
  // The live speed across the set, summed by the page from the rows.
  live: { type: Object, default: () => ({ up: 0, down: 0 }) },
})
const nf = (n) => Number(n || 0).toLocaleString(store.locale)
const rate = (v) => `${bytes(v || 0, store.locale)}/s`
const runState = computed(() =>
  t('iface.stat.runState', {
    run: props.rows.filter((i) => i.enabled && i.running).length,
    stop: props.rows.filter((i) => !(i.enabled && i.running)).length,
  }),
)
</script>

<template>
  <div class="grid g4">
    <div class="card stat">
      <div class="lbl"><span class="dot"></span>{{ t('iface.stat.totalDownUp') }}</div>
      <div class="val ltr" style="font-size: 19px">
        <span class="ok-t">↑ {{ bytes(totals.up, store.locale) }}</span>
        <span class="faint"> / </span>
        <span class="sky-t">↓ {{ bytes(totals.down, store.locale) }}</span>
      </div>
    </div>
    <div class="card stat">
      <div class="lbl"><span class="dot acc"></span>{{ t('iface.stat.totalUsage') }}</div>
      <div class="val">{{ bytes(totals.used, store.locale) }}</div>
    </div>
    <div class="card stat">
      <div class="lbl"><span class="dot ok"></span>{{ t('iface.stat.liveSpeed') }}</div>
      <div class="val ltr" style="font-size: 19px">
        <span class="ok-t">↑ {{ rate(live.up) }}</span>
        <span class="faint"> · </span>
        <span class="sky-t">↓ {{ rate(live.down) }}</span>
      </div>
    </div>
    <div class="card stat">
      <div class="lbl"><span class="dot gray"></span>{{ t('nav.interfaces') }}</div>
      <div class="val">{{ nf(totals.count) }}</div>
      <div class="foot faint">{{ runState }}</div>
    </div>
  </div>
</template>
