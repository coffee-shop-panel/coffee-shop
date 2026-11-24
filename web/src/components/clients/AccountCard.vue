<script setup>
// A reseller's own account, over their customers: how many they may still
// add, how much traffic and how many days they have left, and why they are
// held back when they are.
import { computed } from 'vue'
import { store, t } from '../../lib/store.js'
import { bytes, dateTime } from '../../lib/format.js'
import { DEPLETING_AT } from '../../lib/clientRow.js'

// The figures name their colours in the app's tag vocabulary; the
// design's meters say the same things with other words.
const METER_TONE = { green: 'ok', orange: 'warn', red: 'bad', purple: 'purple' }
const meterTone = (c) => METER_TONE[c] || 'gray'

const props = defineProps({ account: { type: Object, default: null } })
const nf = (n) => Number(n || 0).toLocaleString(store.locale)

const accountStrip = computed(() => {
  const a = props.account
  if (!a) return null
  const DAY = 86400e3
  const left = a.quotaBytes ? Math.max(0, a.quotaBytes - a.usedBytes) : null
  const spentPct = a.quotaBytes ? Math.min(100, Math.round((a.usedBytes / a.quotaBytes) * 100)) : null
  const days = a.expiresAt ? Math.ceil((new Date(a.expiresAt).getTime() - Date.now()) / DAY) : null
  let reason = ''
  if (!a.enabled) reason = t('admins.switchedOff')
  else if (days !== null && days <= 0) reason = t('admins.termEnded')
  else if (a.quotaBytes && a.usedBytes >= a.quotaBytes) reason = t('admins.allowanceSpent')
  return {
    customers: a.clientLimit ? `${nf(a.users)} / ${nf(a.clientLimit)}` : `${nf(a.users)} / ∞`,
    customersColor: a.clientLimit && a.users >= a.clientLimit ? 'orange' : 'green',
    // The design draws each figure with a meter under it. The proportions
    // are the account's own: customers and traffic against their ceilings,
    // and time against a month -- the scale the design's card reads on,
    // since a term's start is not the panel's to state here.
    customersPct: a.clientLimit ? Math.min(100, Math.round((a.users / a.clientLimit) * 100)) : null,
    traffic: left === null ? '∞' : bytes(left, store.locale),
    trafficOf: a.quotaBytes ? bytes(a.quotaBytes, store.locale) : '',
    trafficColor: left === null ? 'purple' : spentPct >= 100 ? 'red' : spentPct >= DEPLETING_AT ? 'orange' : 'green',
    trafficPct: spentPct === null ? null : Math.max(0, 100 - spentPct),
    time: days === null ? '∞' : days <= 0 ? t('admins.termEnded') : t('admins.daysLeft', { n: nf(days) }),
    timeColor: days === null ? 'purple' : days <= 0 ? 'red' : days <= 7 ? 'orange' : 'green',
    timePct: days === null ? null : Math.max(0, Math.min(100, Math.round((days / 30) * 100))),
    timeTitle: a.expiresAt ? dateTime(a.expiresAt, store.locale) : '',
    reason,
  }
})
</script>

<template>
  <div v-if="accountStrip" class="account-card">
    <!-- Why the account is held back, said first and in the design's
         warning band, as the capacity page draws it. -->
    <div v-if="accountStrip.reason" class="banner warn">
      <span class="dtag warn">{{ t('admins.you.title') }}</span>
      <div>{{ t('admins.you.barred', { reason: accountStrip.reason }) }}</div>
    </div>

    <!-- What was bought and what is left of it: the design's three
         meter cards -- users, traffic, time. -->
    <div class="grid g3 mb2">
      <section class="card">
        <div class="cb stack">
          <span class="kicker">{{ t('admins.you.customers') }}</span>
          <div class="stat" style="padding: 0">
            <div class="val ltr">{{ accountStrip.customers }}</div>
          </div>
          <div v-if="accountStrip.customersPct !== null" class="dmeter">
            <i :class="accountStrip.customersColor === 'orange' ? 'warn' : 'ok'" :style="{ width: accountStrip.customersPct + '%' }"></i>
          </div>
        </div>
      </section>
      <section class="card">
        <div class="cb stack">
          <span class="kicker">{{ t('admins.you.trafficLeft') }}</span>
          <div class="stat" style="padding: 0">
            <div class="val ltr">{{ accountStrip.traffic }} <small v-if="accountStrip.trafficOf">/ {{ accountStrip.trafficOf }}</small></div>
          </div>
          <div v-if="accountStrip.trafficPct !== null" class="dmeter">
            <i :class="meterTone(accountStrip.trafficColor)" :style="{ width: accountStrip.trafficPct + '%' }"></i>
          </div>
        </div>
      </section>
      <section class="card">
        <div class="cb stack">
          <span class="kicker">{{ t('admins.you.timeLeft') }}</span>
          <div class="stat" style="padding: 0">
            <div class="val" dir="auto">{{ accountStrip.time }}</div>
          </div>
          <div v-if="accountStrip.timePct !== null" class="dmeter">
            <i :class="meterTone(accountStrip.timeColor)" :style="{ width: accountStrip.timePct + '%' }"></i>
          </div>
          <span v-if="accountStrip.timeTitle" class="faint xs">{{ accountStrip.timeTitle }}</span>
        </div>
      </section>
    </div>
  </div>
</template>

<style scoped>
.account-card { margin-bottom: 2px; }
</style>
