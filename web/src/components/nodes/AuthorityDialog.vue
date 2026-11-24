<script setup>
// This panel's own certificate authority, on its own: the same reading
// the node form offers when mutual TLS is chosen, lifted to where the
// design puts it — beside the token button, since both are how a node
// proves itself to this panel. Read-only, as it is there: the key
// behind it stays in this panel.
import { onMounted, ref } from 'vue'
import { api } from '../../lib/api.js'
import { t, notify } from '../../lib/store.js'
import AntIcon from '../AntIcon.vue'
import Icon from '../Icon.vue'

const emit = defineEmits(['close'])

const authority = ref('')
const busy = ref(false)

async function load() {
  busy.value = true
  try {
    const res = await api.get('/api/nodes/mtls/authority')
    authority.value = res?.caCert || ''
  } catch (e) {
    notify(e.message, 'error')
  } finally {
    busy.value = false
  }
}

async function copy() {
  try {
    await navigator.clipboard.writeText(authority.value)
    notify(t('node.authorityCopied'), 'success')
  } catch {
    notify(t('action.copyFailed'), 'error')
  }
}

onMounted(load)
</script>

<template>
  <div class="modal-backdrop" @click.self="emit('close')">
    <div class="modal" role="dialog" aria-modal="true">
      <div class="card-head">
        <h2>{{ t('node.authority') }}</h2>
        <button class="btn sm icon ghost spacer" :aria-label="t('action.cancel')" @click="emit('close')">
          <Icon name="close" :size="14" />
        </button>
      </div>
      <div class="card-body">
        <textarea
          v-model="authority"
          rows="6"
          readonly
          class="atextarea ltr mono"
          :placeholder="t('node.authorityLoading')"
        ></textarea>
        <p class="hint">{{ t('node.authorityHint') }}</p>
      </div>
      <div class="modal-foot">
        <button class="btn" :disabled="busy" @click="load">
          <span v-if="busy" class="spin sm"></span>
          <template v-else>
            <AntIcon name="SafetyCertificateOutlined" /><span>{{ t('node.authorityLoad') }}</span>
          </template>
        </button>
        <button class="btn primary" :disabled="!authority" @click="copy">
          <AntIcon name="CopyOutlined" /><span>{{ t('action.copy') }}</span>
        </button>
      </div>
    </div>
  </div>
</template>
