<script setup>
// The two small cards the overview's side stack closes with: how long
// things have been up (the panel, the host, and what the panel itself
// holds), and the server's addresses, blurred until asked for. Split
// from the one strip they used to share so each sits in the stack the
// design puts beside the throughput chart.
import { computed, ref } from 'vue'
import { store, t } from '../../lib/store.js'
import { bytes } from '../../lib/format.js'
import Icon from '../Icon.vue'

const props = defineProps({
  system: { type: Object, required: true },
  panel: { type: Object, default: null },
})
const sys = computed(() => props.system)
const showIp = ref(false)
const nf = (n) => Number(n || 0).toLocaleString(store.locale)

function duration(sec) {
  const s = Number(sec || 0)
  const d = Math.floor(s / 86400)
  const h = Math.floor((s % 86400) / 3600)
  const m = Math.floor((s % 3600) / 60)
  if (d) return `${d}d ${h}h`
  if (h) return `${h}h ${m}m`
  return `${m}m`
}

const ipv4 = computed(() => (sys.value?.ipv4 || [])[0] || '—')
const ipv6 = computed(() => (sys.value?.ipv6 || [])[0] || '—')
</script>

<template>
  <section class="card">
    <div class="ch">
      <h3>{{ t('overview.uptime') }}</h3>
    </div>
    <div class="cb tight">
      <dl class="kv">
        <dt>{{ t('overview.panelUptime') }}</dt>
        <dd class="ltr">{{ duration(panel.uptimeSec) }}</dd>
        <dt>{{ t('overview.hostUptime') }}</dt>
        <dd class="ltr">{{ duration(sys.host.uptimeSec) }}</dd>
        <dt>{{ t('overview.panelMemory') }}</dt>
        <dd>{{ bytes(sys.panel.memoryBytes, store.locale) }}</dd>
        <dt>{{ t('overview.goroutines') }}</dt>
        <dd>{{ nf(sys.panel.goroutines) }}</dd>
      </dl>
    </div>
  </section>

  <section class="card">
    <div class="ch">
      <h3>{{ t('overview.addresses') }}</h3>
      <button
        class="act spacer"
        :aria-label="t('overview.toggleAddresses')"
        :aria-pressed="showIp"
        @click="showIp = !showIp"
      >
        <Icon :name="showIp ? 'eyeOff' : 'eye'" :size="15" />
      </button>
    </div>
    <div class="cb tight">
      <dl class="kv">
        <dt>IPv4</dt>
        <!-- Blurred rather than replaced: the shape stays, the value does
             not, which is what an overview shown on a shared screen needs. -->
        <dd class="mono blur" :class="{ shown: showIp }" style="text-align: end">{{ ipv4 }}</dd>
        <dt>IPv6</dt>
        <dd class="mono blur" :class="{ shown: showIp }" style="text-align: end">{{ ipv6 }}</dd>
      </dl>
    </div>
  </section>
</template>
