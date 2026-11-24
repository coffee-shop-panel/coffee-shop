<script setup>
// The counts over the customer list, as the design's stat cards: everyone,
// online, ended, running low, switched off, active — a dot, the count,
// and the one line that says what the count means.
import { store, t } from '../../lib/store.js'

defineProps({ stats: { type: Object, required: true } })
const nf = (n) => Number(n || 0).toLocaleString(store.locale)
</script>

<template>
  <div class="grid g6 mb">
    <div class="card stat">
      <div class="lbl"><span class="ddot gray"></span>{{ t('nav.clients') }}</div>
      <div class="val">{{ nf(stats.clients) }}</div>
      <div class="foot">{{ t('stat.footClients') }}</div>
    </div>
    <div class="card stat">
      <div class="lbl"><span class="ddot ok live"></span>{{ t('status.online') }}</div>
      <div class="val">{{ nf(stats.online) }}</div>
      <div class="foot">{{ t('stat.footOnline') }}</div>
    </div>
    <div class="card stat" :class="{ hot: (stats.exhausted || 0) + (stats.expired || 0) }">
      <div class="lbl"><span class="ddot bad"></span>{{ t('stat.depleted') }}</div>
      <div class="val">{{ nf((stats.exhausted || 0) + (stats.expired || 0)) }}</div>
      <div class="foot">{{ t('stat.footEnded') }}</div>
    </div>
    <div class="card stat" :class="{ hot: stats.depleting }">
      <div class="lbl"><span class="ddot warn"></span>{{ t('stat.depleting') }}</div>
      <div class="val">{{ nf(stats.depleting) }}</div>
      <div class="foot">{{ t('stat.footDepletingSell') }}</div>
    </div>
    <div class="card stat">
      <div class="lbl"><span class="ddot gray"></span>{{ t('status.disabled') }}</div>
      <div class="val">{{ nf(stats.disabled) }}</div>
      <div class="foot">{{ t('stat.footDisabled') }}</div>
    </div>
    <div class="card stat">
      <div class="lbl"><span class="ddot acc"></span>{{ t('status.active') }}</div>
      <div class="val">{{ nf(stats.active) }}</div>
      <div class="foot">{{ t('stat.footActive') }}</div>
    </div>
  </div>
</template>
