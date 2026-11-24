<script setup>
// Who did what on this panel, newest first: every change an operator or a
// reseller made, and every one the panel refused, with the reason it gave.
// The owner's page alone -- it names accounts and addresses that are nobody
// else's business, and the route is unmarked, which is what makes it so.
import { computed, onMounted, ref } from 'vue'
import { api } from '../lib/api.js'
import { useDelayed } from '../lib/live.js'
import { store, t, notify } from '../lib/store.js'
import { dateTime } from '../lib/format.js'
import ErrorState from '../components/ErrorState.vue'
import PageSpin from '../components/PageSpin.vue'

// One page of the log. It has to agree with the default api.activity asks
// for: "a full page came back" is how this page knows there may be more.
const PAGE = 100

const items = ref([])
const loading = ref(true)
const loadError = ref(null)
const moreBusy = ref(false)
const hasMore = ref(false)

const showSkeleton = useDelayed(computed(() => loading.value && !items.value.length))

onMounted(load)

async function load() {
  loading.value = true
  try {
    const res = await api.activity()
    items.value = res?.items || []
    hasMore.value = items.value.length >= PAGE
    loadError.value = null
  } catch (e) {
    loadError.value = e
  } finally {
    loading.value = false
  }
}

// Older entries are appended, keyed by the oldest id already shown. The log
// only ever grows at the top, so an id stays put where an offset would
// drift every time somebody does something.
async function loadMore() {
  const last = items.value[items.value.length - 1]
  if (!last || moreBusy.value) return
  moreBusy.value = true
  try {
    const res = await api.activity(last.id)
    const older = res?.items || []
    items.value = [...items.value, ...older]
    hasMore.value = older.length >= PAGE
  } catch (e) {
    notify(e.message, 'error')
  } finally {
    moreBusy.value = false
  }
}

// The status as a tag: green it was done, orange the panel said no to the
// request itself, red the panel failed. Anything else -- a redirect -- is
// neither, and stays grey.
function statusTone(status) {
  if (status >= 200 && status < 300) return 'green'
  if (status >= 400 && status < 500) return 'orange'
  if (status >= 500) return 'red'
  return 'grey'
}
</script>

<template>
  <div class="card">
    <PageSpin v-if="showSkeleton" />
    <div v-else-if="loading" class="empty"></div>
    <ErrorState v-else-if="loadError" :error="loadError" @retry="load" />
    <template v-else>
      <div class="table-wrap">
        <table>
          <thead>
            <tr>
              <th class="w-md">{{ t('activity.when') }}</th>
              <th>{{ t('activity.actor') }}</th>
              <th>{{ t('activity.action') }}</th>
              <th class="w-md">{{ t('activity.from') }}</th>
              <th class="w-sm">{{ t('activity.result') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-if="!items.length" class="empty-row">
              <td colspan="5">
                <div class="card-empty">
                  <div>{{ t('activity.empty') }}</div>
                </div>
              </td>
            </tr>
            <tr v-for="a in items" :key="a.id">
              <td class="small muted">{{ dateTime(a.at, store.locale) }}</td>
              <td>
                <span>{{ a.actor }}</span>
                <!-- The role is said beside the name because the same person
                     can hold more than one, and what they were acting as is
                     half of what the row is evidence of. -->
                <span v-if="a.role" class="muted small"> ({{ a.role }})</span>
              </td>
              <td>
                <!-- The request as it was made, method and path, left-to-right
                     and in the mono face: it is a line out of a log, not prose. -->
                <span class="mono">{{ a.action }}</span>
                <!-- The reason sits under the action it belongs to, so a
                     refusal can be read without hunting across the row. -->
                <div v-if="a.reason" class="sub muted small">{{ a.reason }}</div>
              </td>
              <td class="small muted ltr">{{ a.ip || '—' }}</td>
              <td>
                <span class="tag num" :class="statusTone(a.status)">{{ a.status }}</span>
              </td>
            </tr>
          </tbody>
        </table>
      </div>

      <div v-if="hasMore" class="card-toolbar">
        <button class="btn" :disabled="moreBusy" @click="loadMore">
          <span v-if="moreBusy" class="spin sm"></span>
          <span>{{ t('activity.more') }}</span>
        </button>
      </div>
    </template>
  </div>
</template>
