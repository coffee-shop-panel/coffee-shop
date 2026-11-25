<script setup>
// The operator's own shop front: the name they sell under and where
// their customers reach them, both of which their customers' subscription
// pages carry -- the name beside the support button, and the contact
// behind it, in place of the panel's own support address. It lives on
// the account page because that is the page every operator has; the
// panel's support setting in Settings stays, as the address customers
// of the owner -- and of a seller who never set one -- are given.
//
// Saved through /me, which writes the caller's own row and answers with
// it, so what the page holds afterwards is what the panel stored.
import { ref, watch } from 'vue'
import { api } from '../../lib/api.js'
import { store, t, notify } from '../../lib/store.js'

const shopName = ref(store.admin?.shopName || '')
const supportContact = ref(store.admin?.supportContact || '')
const busy = ref(false)

// The record can land after the page (a slow /me, a sign-in that
// resolves behind it): take the fields from it when it does, unless the
// operator has already typed over them.
let touched = false
watch(
  () => store.admin,
  (a) => {
    if (!a || touched) return
    shopName.value = a.shopName || ''
    supportContact.value = a.supportContact || ''
  },
)

async function save() {
  busy.value = true
  try {
    store.admin = await api.updateMe({
      shopName: shopName.value.trim(),
      supportContact: supportContact.value.trim(),
    })
    shopName.value = store.admin?.shopName || ''
    supportContact.value = store.admin?.supportContact || ''
    notify(t('account.shopSaved'), 'success')
  } catch (e) {
    notify(e.message, 'error')
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div class="setting-list-item"><div class="arow">
    <div class="acol"><div class="setting-list-meta"><div class="setting-list-title">{{ t('account.shopName') }}</div><div class="setting-list-description">{{ t('account.shopNameDesc') }}</div></div></div>
    <div class="acol"><label class="ainput block"><input v-model="shopName" maxlength="64" :placeholder="store.admin?.username || ''" @input="touched = true" /></label></div>
  </div></div>
  <div class="setting-list-item"><div class="arow">
    <div class="acol"><div class="setting-list-meta"><div class="setting-list-title">{{ t('account.supportContact') }}</div><div class="setting-list-description">{{ t('account.supportContactDesc') }}</div></div></div>
    <div class="acol"><label class="ainput block"><input v-model="supportContact" maxlength="256" class="ltr" :placeholder="t('account.supportContactPlaceholder')" @input="touched = true" /></label></div>
  </div></div>
  <div class="setting-list-item"><div class="arow">
    <div class="acol"></div>
    <div class="acol"><button class="abtn primary" :disabled="busy" @click="save"><span v-if="busy" class="spin sm"></span>{{ t('action.save') }}</button></div>
  </div></div>
</template>
