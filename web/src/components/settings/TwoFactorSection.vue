<script setup>
// The second factor: its switch, and the modal that enrols or ends it. A
// section of its own rather than part of the security tab so the account
// page can hold the same switch -- the enrolment is the operator's own,
// and every operator has an account page, where only the owner has the
// settings page.
import { onMounted, ref } from 'vue'
import { api } from '../../lib/api.js'
import { makeQR } from '../../lib/qr.js'
import { t, notify } from '../../lib/store.js'
import AntIcon from '../AntIcon.vue'
import Toggle from '../Toggle.vue'

const twoFactor = ref(false)
const tfa = ref(null) // { type: 'set'|'delete', token, uri, qr, code }

onMounted(loadMe)

async function loadMe() {
  try {
    const me = await api.get('/api/auth/me')
    twoFactor.value = !!me.twoFactor
  } catch {
    /* the section is still usable without it */
  }
}
async function toggleTwoFactor() {
  if (twoFactor.value) {
    tfa.value = { type: 'delete', code: '' }
    return
  }
  try {
    const res = await api.post('/api/auth/totp/start')
    const qr = await makeQR(res.uri)
    tfa.value = { type: 'set', token: res.secret, uri: res.uri, qr, code: '' }
  } catch (e) {
    notify(e.message, 'error')
  }
}
async function confirmTfa() {
  const m = tfa.value
  try {
    if (m.type === 'set') {
      await api.post('/api/auth/totp/confirm', { secret: m.token, code: m.code })
      twoFactor.value = true
      notify(t('settings.twoFactorEnabled'), 'success')
    } else {
      await api.post('/api/auth/totp/disable', { code: m.code, password: m.password || '' })
      twoFactor.value = false
      notify(t('settings.twoFactorDisabled'), 'success')
    }
    tfa.value = null
  } catch (e) {
    notify(e.message, 'error')
  }
}
async function copyText(text) {
  try {
    await navigator.clipboard.writeText(text)
    notify(t('action.copied'), 'success')
  } catch {
    notify(t('share.copyFailed'), 'error')
  }
}
</script>

<template>
  <div class="setting-list-item"><div class="arow">
    <div class="acol"><div class="setting-list-meta"><div class="setting-list-title">{{ t('set.security.twoFactorEnable') }}</div><div class="setting-list-description">{{ t('set.security.twoFactorEnableDesc') }}</div></div></div>
    <div class="acol"><Toggle :model-value="twoFactor" :label="t('set.security.twoFactorEnable')" @update:model-value="toggleTwoFactor" /></div>
  </div></div>

  <!-- Two-factor: their modal, steps and QR and code. -->
  <div v-if="tfa" class="amodal-backdrop" @click.self="tfa = null">
    <div class="amodal" role="dialog" aria-modal="true">
      <div class="amodal-head"><h2 class="amodal-title">{{ tfa.type === 'set' ? t('set.twoFactorModalSetTitle') : t('set.twoFactorModalDeleteTitle') }}</h2><button type="button" class="amodal-close" :aria-label="t('common.close')" @click="tfa = null"><AntIcon name="CloseOutlined" /></button></div>
      <div class="amodal-body">
        <template v-if="tfa.type === 'set'">
          <p>{{ t('set.twoFactorModalSteps') }}</p>
          <hr class="adivider" />
          <p>{{ t('set.twoFactorModalFirstStep') }}</p>
          <div class="qr-wrap" role="button" tabindex="0" :aria-label="t('set.copy')" @click="copyText(tfa.token)" @keydown.enter="copyText(tfa.token)">
            <img class="qr-code" :src="tfa.qr.dataUrl" width="180" height="180" alt="" />
            <span class="qr-token ltr">{{ tfa.token }}</span>
          </div>
          <hr class="adivider" />
          <p>{{ t('set.twoFactorModalSecondStep') }}</p>
          <label class="ainput block"><input v-model="tfa.code" inputmode="numeric" maxlength="6" class="ltr" :aria-label="t('set.twoFactorCode')" @keydown.enter="confirmTfa" /></label>
        </template>
        <template v-else>
          <p>{{ t('set.twoFactorModalDeleteDesc') }}</p>
          <label class="ainput block"><input v-model="tfa.code" class="ltr" :placeholder="t('set.twoFactorCode')" :aria-label="t('set.twoFactorCode')" /></label>
          <label class="ainput block" style="margin-top: 8px"><input v-model="tfa.password" type="password" autocomplete="current-password" :placeholder="t('set.password')" /></label>
        </template>
      </div>
      <div class="amodal-foot">
        <button class="abtn" @click="tfa = null">{{ t('set.cancel') }}</button>
        <button class="abtn primary" :disabled="tfa.type === 'set' ? !/^\d{6}$/.test(tfa.code || '') : !(tfa.code || tfa.password)" @click="confirmTfa">{{ t('set.confirm') }}</button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.qr-wrap { display: flex; flex-direction: column; align-items: center; gap: 12px; cursor: pointer; }
.qr-code { display: block; border-radius: 4px; background: #fff; }
.qr-token { font-size: 12px; font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace; word-break: break-all; text-align: center; }
.adivider { margin: 24px 0; border: 0; border-top: 1px solid var(--line-soft); }
.amodal-body p { margin: 0 0 1em; }
</style>
