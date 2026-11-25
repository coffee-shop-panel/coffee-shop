<script setup>
// The owner's word to the room: one announcement, standing over
// every operator's dashboard until that operator dismisses it.
//
// The card is the owner's alone in practice -- the page it sits on
// is the operators page, which only the owner opens, and the
// endpoints behind it refuse everyone else again. It keeps its own
// state and its own loading, the way the list above it keeps its:
// publish, and the history below is the record of what was said.
import { computed, onMounted, ref } from 'vue'
import { api } from '../../lib/api.js'
import { store, t, notify } from '../../lib/store.js'
import { date } from '../../lib/format.js'
import Icon from '../Icon.vue'
import ConfirmDialog from '../ConfirmDialog.vue'

const items = ref([])
const text = ref('')
const busy = ref(false)
const loadError = ref(null)
const takingBack = ref(null)

async function load() {
  try {
    items.value = (await api.announcements()) || []
    loadError.value = null
  } catch (err) {
    loadError.value = err
  }
}
onMounted(load)

const ready = computed(() => text.value.trim() !== '')

async function publish() {
  const body = text.value.trim()
  if (!body || busy.value) return
  busy.value = true
  try {
    await api.announcementCreate(body)
    text.value = ''
    notify(t('announce.published'), 'success')
    await load()
  } catch (err) {
    notify(err.message, 'error')
  } finally {
    busy.value = false
  }
}

async function takeBack() {
  const row = takingBack.value
  if (!row) return
  busy.value = true
  try {
    await api.announcementDelete(row.id)
    takingBack.value = null
    notify(t('announce.deleted'), 'success')
    await load()
  } catch (err) {
    notify(err.message, 'error')
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <section class="card" style="margin-top: 12px">
    <div class="ch">
      <h3>{{ t('announce.title') }}</h3>
    </div>
    <div class="cb">
      <p class="muted small" style="margin-top: 0">{{ t('announce.lede') }}</p>

      <div class="field">
        <textarea
          v-model="text"
          rows="3"
          :placeholder="t('announce.placeholder')"
          :aria-label="t('announce.title')"
        ></textarea>
      </div>
      <div style="display: flex; justify-content: flex-end">
        <button class="btn primary" :disabled="busy || !ready" @click="publish">
          <span v-if="busy" class="spin sm"></span>
          <Icon v-else name="send" :size="14" />
          <span>{{ t('announce.publish') }}</span>
        </button>
      </div>

      <p v-if="loadError" class="muted small">
        {{ loadError.message }}
        <button type="button" class="btn xs" @click="load">{{ t('action.retry') }}</button>
      </p>
      <ul v-else-if="items.length" class="list" style="margin-top: 10px">
        <li v-for="a in items" :key="a.id">
          <div class="col" style="flex: 1">
            <span style="white-space: pre-line">{{ a.text }}</span>
            <span class="faint xs">{{ date(a.createdAt, store.locale) }}</span>
          </div>
          <button
            type="button"
            class="btn xs danger"
            :disabled="busy"
            :aria-label="t('action.delete')"
            @click="takingBack = a"
          >
            <Icon name="trash" :size="13" />
          </button>
        </li>
      </ul>
      <p v-else class="muted small">{{ t('announce.empty') }}</p>
    </div>

    <ConfirmDialog
      :open="!!takingBack"
      :title="t('announce.deleteTitle')"
      :body="t('announce.deleteBody')"
      :subject="takingBack?.text || ''"
      :confirm-label="t('announce.deleteConfirm')"
      :busy="busy"
      @confirm="takeBack"
      @cancel="takingBack = null"
    />
  </section>
</template>
