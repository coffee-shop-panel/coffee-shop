<script setup>
// Move Customers: everyone on this tunnel put on another one, for good.
// Attach (MoveDialog) adds a tunnel beside the one a customer already has;
// a move takes them off this one, which is what emptying a tunnel before
// retiring it takes. Their subscription link is theirs and does not change:
// only the tunnel their devices sit on does.
import { computed, ref } from 'vue'
import { api } from '../../lib/api.js'
import { t, tn, notify } from '../../lib/store.js'
import Icon from '../Icon.vue'

const props = defineProps({
  iface: { type: Object, required: true },
  interfaces: { type: Array, default: () => [] },
})
const emit = defineEmits(['close', 'done'])
const busy = ref(false)
const target = ref(null)

const count = computed(() => props.iface.clients || 0)
// Only another WireGuard tunnel can take them: a move re-seats WireGuard
// devices. A disabled one is left out too -- moving customers onto a tunnel
// that is not running trades one problem for another.
const targets = computed(() =>
  props.interfaces.filter((x) => x.id !== props.iface.id && x.protocol === 'wireguard' && x.enabled),
)

async function submit() {
  if (!target.value || !count.value) return
  busy.value = true
  try {
    const res = await api.moveInterfaceClients(props.iface.id, target.value)
    notify(t('iface.moveDone', { moved: res?.moved || 0 }), 'success')
    if ((res?.failed || 0) > 0) notify(t('iface.moveFailed', { failed: res.failed }), 'error')
    emit('done')
    emit('close')
  } catch (err) {
    notify(err.message, 'error')
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <!-- Move Customers: a target tunnel to pick, and what the move means. -->
  <div class="modal-backdrop" @click.self="emit('close')">
    <div class="modal narrow" role="dialog" aria-modal="true" aria-labelledby="mvc-title">
      <div class="card-head">
        <h2 id="mvc-title">{{ t('iface.moveTitle') }} — {{ iface.name }}</h2>
        <button class="act" :aria-label="t('common.close')" @click="emit('close')"><Icon name="close" :size="16" /></button>
      </div>
      <div class="card-body">
        <p class="muted small">{{ t('iface.moveHint') }}</p>
        <p v-if="!count" class="muted small">{{ t('iface.moveEmpty') }}</p>
        <template v-else>
          <div class="field">
            <label for="mvc-target">{{ t('iface.moveTo') }}</label>
            <select id="mvc-target" v-model="target">
              <option :value="null" disabled>—</option>
              <option v-for="o in targets" :key="o.id" :value="o.id">{{ o.name }} · {{ o.protocol }}:{{ o.listenPort }}</option>
            </select>
          </div>
          <p class="muted small">{{ tn('interface.nClients', count) }}</p>
        </template>
      </div>
      <div class="modal-foot">
        <button type="button" class="btn" @click="emit('close')">{{ t('common.close') }}</button>
        <button class="btn primary" :disabled="busy || !target || !count" @click="submit">
          <span v-if="busy" class="spin"></span>
          <template v-else>{{ t('iface.move') }}</template>
        </button>
      </div>
    </div>
  </div>
</template>
