<script setup>
// The fingerprint door's keys: the operator's own passkeys, their list and
// their making and ending. It is a section of its own rather than part of
// the security tab so the account page can hold the same list -- a passkey
// is registered by the person who will use it, and every operator has one
// of those pages, where only the owner has the settings page. One list,
// two doors to it, and the same rows behind both.
//
// Offered only where they can work -- a browser with the API on an
// encrypted origin -- because the panel refuses the ceremonies anywhere
// else; where they cannot, the section says so instead of offering a
// button that would fail.
import { onMounted, ref } from 'vue'
import { api } from '../../lib/api.js'
import { passkeySupported, createPasskey } from '../../lib/passkey.js'
import { relative } from '../../lib/format.js'
import { store, t, notify } from '../../lib/store.js'
import ConfirmDialog from '../ConfirmDialog.vue'

const pkSupported = passkeySupported()
const passkeys = ref([])
const pkBusy = ref(false)
const revokingPk = ref(null)

onMounted(loadPasskeys)

async function loadPasskeys() {
  if (!pkSupported) return
  try {
    passkeys.value = await api.passkeys()
  } catch {
    passkeys.value = []
  }
}
async function addPasskey() {
  pkBusy.value = true
  try {
    // Named by its kind and its day: told apart in the list without the
    // operator having to invent a label at the fingerprint prompt.
    const name = `${t('set.security.passkeyName')} · ${new Date().toLocaleDateString(store.locale)}`
    const options = await api.passkeyRegisterOptions(name)
    const answer = await createPasskey(options)
    await api.passkeyRegisterVerify(answer)
    notify(t('set.security.passkeyAdded'), 'success')
    await loadPasskeys()
  } catch (e) {
    notify(e?.name === 'NotAllowedError' ? t('auth.passkeyCancelled') : e.message, 'error')
  } finally {
    pkBusy.value = false
  }
}
async function doRevokePk() {
  const pk = revokingPk.value
  revokingPk.value = null
  try {
    await api.passkeyDelete(pk.id)
    notify(t('set.security.passkeyRevoked', { n: pk.name }), 'success')
    await loadPasskeys()
  } catch (e) {
    notify(e.message, 'error')
  }
}
function tokenDate(iso) {
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? '' : d.toLocaleString(store.locale)
}
</script>

<template>
  <div class="setting-list-item"><div class="arow">
    <div class="acol"><div class="setting-list-meta"><div class="setting-list-title">{{ t('set.security.passkey') }}</div><div class="setting-list-description">{{ t('set.security.passkeyDesc') }}</div></div></div>
    <div class="acol"><button class="abtn primary" :disabled="pkBusy || !pkSupported" @click="addPasskey"><span v-if="pkBusy" class="spin sm"></span>{{ t('set.security.passkeyAdd') }}</button></div>
  </div></div>
  <div v-if="!pkSupported" class="setting-list-item"><div class="arow">
    <div class="acol"><div class="setting-list-meta"><div class="setting-list-description">{{ t('set.security.passkeyUnavailable') }}</div></div></div>
  </div></div>
  <div v-else-if="passkeys.length" class="api-token-section">
    <div v-for="pk in passkeys" :key="pk.id" class="api-token-row">
      <div class="api-token-row-head">
        <div class="api-token-name-wrap">
          <span class="api-token-name">{{ pk.name }}</span>
          <span class="api-token-created">{{ tokenDate(pk.createdAt) }} · {{ pk.lastUsedAt ? relative(pk.lastUsedAt, store.locale) : t('settings.tokenNeverUsed') }}</span>
        </div>
        <div class="api-token-actions">
          <button class="abtn text sm danger-text" @click="revokingPk = pk">{{ t('action.revoke') }}</button>
        </div>
      </div>
    </div>
  </div>
  <div v-else class="setting-list-item"><div class="arow">
    <div class="acol"><div class="setting-list-meta"><div class="setting-list-description">{{ t('set.security.passkeyEmpty') }}</div></div></div>
  </div></div>

  <ConfirmDialog
    :open="!!revokingPk"
    :title="t('set.security.passkeyRevokeTitle')"
    :body="t('set.security.passkeyRevokeBody', { n: revokingPk?.name || '' })"
    :confirm-label="t('action.revoke')"
    :danger="true"
    @confirm="doRevokePk"
    @cancel="revokingPk = null"
  />
</template>

<style scoped>
.api-token-section { padding: 8px 20px 16px; display: flex; flex-direction: column; gap: 12px; }
.api-token-row { border: 1px solid var(--line-soft); border-radius: var(--r); background: var(--surface-2); padding: 12px 14px; display: flex; flex-direction: column; gap: 8px; transition: opacity 0.15s; }
.api-token-row-head { display: flex; align-items: center; justify-content: space-between; gap: 8px; flex-wrap: wrap; }
.api-token-name-wrap { display: flex; flex-direction: column; gap: 2px; }
.api-token-name { font-weight: 600; font-size: 13.5px; }
.api-token-created { font-size: 11px; opacity: 0.55; }
.api-token-actions { display: flex; align-items: center; gap: 8px; }
</style>
