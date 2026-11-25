<script setup>
// The monthly report: one month of the operator's own business -- the
// customers they made and the allowance those sales committed, the
// customers their sales bot renewed, and what all of theirs carried,
// day by day and by heaviest customer. The endpoint behind it counts
// over the caller's own customers alone, so the page is the operator's
// month and nobody else's, whichever month they turn to.
import { ref, computed, onMounted } from 'vue'
import { RouterLink } from 'vue-router'
import { api } from '../lib/api.js'
import { useDelayed } from '../lib/live.js'
import { store, t, notify } from '../lib/store.js'
import { bytes } from '../lib/format.js'
import AntIcon from '../components/AntIcon.vue'
import ErrorState from '../components/ErrorState.vue'
import PageSpin from '../components/PageSpin.vue'

// The month on screen, "YYYY-MM". It starts at this month and walks
// backwards freely; forwards stops at this month, because a month that
// has not happened has no report worth inventing. Local, not UTC: in
// Tehran the first hours of the 1st are still last month in UTC.
function monthOf(d) {
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}`
}
const thisMonth = monthOf(new Date())
const month = ref(thisMonth)
const data = ref(null)
const loading = ref(true)
const loadError = ref(null)

async function load() {
  loading.value = true
  try {
    data.value = await api.overviewReport(month.value)
    loadError.value = null
  } catch (err) {
    loadError.value = err
    notify(err.message, 'error')
  } finally {
    loading.value = false
  }
}
onMounted(load)

const showSkeleton = useDelayed(computed(() => loading.value && !data.value))

function shift(delta) {
  const [y, m] = month.value.split('-').map(Number)
  const next = monthOf(new Date(y, m - 1 + delta, 1))
  if (next > thisMonth) return
  month.value = next
  load()
}
const isThisMonth = computed(() => month.value === thisMonth)
const monthLabel = computed(() => {
  const [y, m] = month.value.split('-').map(Number)
  return new Intl.DateTimeFormat(store.locale, { month: 'long', year: 'numeric' }).format(new Date(y, m - 1, 1))
})

const nf = (n) => Number(n || 0).toLocaleString(store.locale)

// A volume as the dashboard's tiles split it: the number large, the
// unit small beside it.
function splitBytes(v) {
  const s = bytes(v || 0, store.locale)
  const at = s.lastIndexOf(' ')
  return at > 0 ? { num: s.slice(0, at), unit: s.slice(at + 1) } : { num: s, unit: '' }
}

const tiles = computed(() => {
  const d = data.value
  if (!d) return []
  const committed = splitBytes(d.createdQuotaBytes)
  const used = splitBytes(d.usedBytes)
  return [
    { key: 'created', label: t('report.created'), num: nf(d.created), unit: '', tone: 'acc', icon: 'TeamOutlined', foot: t('report.footCreated') },
    { key: 'committed', label: t('report.committed'), num: committed.num, unit: committed.unit, tone: 'sky', icon: 'DatabaseOutlined', foot: t('report.footCommitted') },
    { key: 'renewals', label: t('report.renewals'), num: nf(d.renewals), unit: '', tone: 'ok', icon: 'SyncOutlined', foot: t('report.footRenewals') },
    { key: 'used', label: t('report.used'), num: used.num, unit: used.unit, tone: 'warn', icon: 'GlobalOutlined', foot: t('report.footUsed') },
  ]
})

const days = computed(() => data.value?.days || [])
const peak = computed(() => Math.max(1, ...days.value.map((d) => d.bytes || 0)))
const top = computed(() => data.value?.top || [])

function barTitle(d) {
  const [, , day] = d.date.split('-')
  return `${Number(day)} · ${bytes(d.bytes || 0, store.locale)}`
}
</script>

<template>
  <PageSpin v-if="showSkeleton" />

  <div v-else-if="loading && !data" class="ov-page cs"></div>

  <ErrorState v-else-if="loadError && !data" :error="loadError" @retry="load()" />

  <div v-else-if="data" class="ov-page cs">
    <div class="ph">
      <div>
        <h2>{{ t('nav.report') }}</h2>
        <p>{{ t('report.lede') }}</p>
      </div>
      <!-- The month switch: back as far as the record goes, forward no
           further than the month being lived. -->
      <div class="actions month-switch">
        <button type="button" class="btn" :aria-label="'‹'" @click="shift(-1)">
          <AntIcon name="LeftOutlined" />
        </button>
        <span class="month-label">{{ monthLabel }}</span>
        <button type="button" class="btn" :disabled="isThisMonth" :aria-label="'›'" @click="shift(1)">
          <AntIcon name="RightOutlined" />
        </button>
      </div>
    </div>

    <div class="grid g4 mb">
      <div v-for="tile in tiles" :key="tile.key" class="card stat">
        <div class="lbl"><span class="ico-tile" :class="tile.tone"><AntIcon :name="tile.icon" /></span>{{ tile.label }}</div>
        <div class="val">{{ tile.num }} <small v-if="tile.unit" class="ceil-of">{{ tile.unit }}</small></div>
        <div class="foot">{{ tile.foot }}</div>
      </div>
    </div>

    <!-- The month by day: one bar per day, every day present, so a quiet
         stretch reads as quiet rather than as missing. -->
    <section class="card">
      <div class="ch">
        <h3>{{ t('report.dailyTitle') }}</h3>
        <span class="dtag gray spacer">{{ bytes(data.usedBytes || 0, store.locale) }}</span>
      </div>
      <div class="cb">
        <div class="bars" role="img" :aria-label="t('report.dailyTitle')">
          <div v-for="d in days" :key="d.date" class="bar-col" :title="barTitle(d)">
            <div class="bar-track">
              <div
                class="bar"
                :class="{ empty: !d.bytes }"
                :style="{ height: d.bytes ? Math.max(3, Math.round((d.bytes / peak) * 100)) + '%' : '0%' }"
              ></div>
            </div>
            <span class="bar-day">{{ Number(d.date.slice(8)) }}</span>
          </div>
        </div>
      </div>
    </section>

    <!-- The heaviest of the month, each opening the customer it names. -->
    <section class="card">
      <div class="ch">
        <h3>{{ t('report.topTitle') }}</h3>
      </div>
      <div class="cb tight">
        <ul v-if="top.length" class="list">
          <li v-for="c in top" :key="c.id">
            <span class="ico-tile acc"><AntIcon name="TeamOutlined" /></span>
            <div class="col" style="flex: 1">
              <RouterLink :to="`/clients/${c.id}`"><b>{{ c.name }}</b></RouterLink>
            </div>
            <span class="when">{{ bytes(c.bytes || 0, store.locale) }}</span>
          </li>
        </ul>
        <p v-else class="muted small" style="padding: 6px 0">{{ t('report.noTop') }}</p>
      </div>
    </section>
  </div>
</template>

<style scoped>
.ov-page {
  --ov-gap: 12px;
  display: flex;
  flex-direction: column;
  gap: var(--ov-gap);
}
.ceil-of {
  font-size: 0.45em;
  font-weight: 500;
  color: var(--muted);
}
.month-switch {
  align-items: center;
  gap: 8px;
}
.month-label {
  min-width: 130px;
  text-align: center;
  font-weight: 600;
}
.bars {
  display: flex;
  align-items: flex-end;
  gap: 3px;
  height: 150px;
}
.bar-col {
  flex: 1;
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 6px;
  height: 100%;
  min-width: 0;
}
.bar-track {
  flex: 1;
  width: 100%;
  display: flex;
  align-items: flex-end;
  background: var(--line-soft);
  border-radius: 4px 4px 0 0;
  overflow: hidden;
}
.bar {
  width: 100%;
  background: var(--accent);
  border-radius: 4px 4px 0 0;
  transition: height 0.3s ease;
}
.bar-day {
  font-size: 10px;
  color: var(--muted);
}
</style>
