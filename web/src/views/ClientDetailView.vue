<script setup>
import { ref, computed, onMounted } from 'vue'
import { useRouter, RouterLink } from 'vue-router'
import { makeQR } from '../lib/qr.js'
import { useDelayed } from '../lib/live.js'
import { api, saveFile } from '../lib/api.js'
import { store, t, tn, notify } from '../lib/store.js'
import { bytes, dateTime, relative, percent, isOnline } from '../lib/format.js'
import Icon from '../components/Icon.vue'
import RingGauge from '../components/RingGauge.vue'
import ConfirmDialog from '../components/ConfirmDialog.vue'
import DeviceSetup from '../components/DeviceSetup.vue'

const props = defineProps({ id: { type: [String, Number], required: true } })
const router = useRouter()

const client = ref(null)
const loading = ref(true)
// Held back a moment. A skeleton that appears and vanishes inside a tenth of a
// second is a flash, and a flash reads as a fault rather than as progress.
const showSkeleton = useDelayed(loading)
const profile = ref(null)
const qr = ref('')
// The size the code must be shown at, which comes from the code: see lib/qr.
const qrSize = ref(340)
const showConfig = ref(false)
const newDevice = ref('')
const busy = ref(false)
const downloading = ref(false)

// The tunnels, to name a device's server beside it and to head the
// usage table's columns. Read in the background: a failure leaves the
// ids showing as numbers rather than failing the page.
const interfaces = ref([])
const ifaceName = (id) => interfaces.value.find((i) => i.id === id)?.name || `#${id}`

// The subscription link and its code, read the way the share dialog
// reads them: the link is the customer's own address, and it exists
// only while subscriptions are switched on.
const subLink = ref('')
const subQr = ref('')
const subQrSize = ref(340)
async function loadSub() {
  try {
    const sub = await api.get('/api/subscription', { background: true }).catch(() => null)
    if (sub?.enabled === false) return
    const res = await api.get(`/api/clients/${props.id}/subscription`, { background: true })
    if (!res?.link) return
    subLink.value = res.link
    const code = await makeQR(res.link)
    subQr.value = code.dataUrl
    subQrSize.value = code.size
  } catch {
    /* no link: the card says so instead of failing the page */
  }
}

// Devices, not accounts. A customer on three servers holds three accounts
// per device, and showing the row count against the device limit would read
// as 6 / 2 for somebody who has two devices.
const deviceCount = computed(
  () => new Set((client.value?.accounts || []).map((a) => a.deviceName.toLowerCase())).size,
)

// Which servers this customer reaches, for the same reason: the device
// table lists one row per account, and the count above it must not.
const serverCount = computed(
  () => new Set((client.value?.accounts || []).map((a) => a.interfaceId)).size,
)

// The header's status: online while any device has handshaken lately,
// the plan's own state otherwise.
const statusTag = computed(() => {
  const c = client.value
  if (!c) return { tone: '', label: '' }
  if ((c.accounts || []).some((a) => isOnline(a.lastHandshake)) && c.status === 'active') {
    return { tone: 'ok', label: t('status.online'), dot: true }
  }
  if (c.status === 'active') return { tone: 'acc', label: t('status.active') }
  if (c.status === 'disabled') return { tone: 'gray', label: t('status.disabled') }
  return { tone: 'bad', label: t(`status.${c.status}`) }
})

// The line under the name: what the operator wrote, when the customer
// was made, and how much of the device allowance is in use.
const metaLine = computed(() => {
  const c = client.value
  if (!c) return ''
  const parts = []
  if (c.note) parts.push(c.note)
  if (c.createdAt) parts.push(`${t('iface.col.createdAt')} ${dateTime(c.createdAt, store.locale)}`)
  parts.push(`${deviceCount.value} / ${c.deviceLimit || '∞'} ${t('device.title')}`)
  if (serverCount.value > 1) parts.push(tn('client.acrossServers', serverCount.value))
  return parts.join(' · ')
})

// Usage split by device and tunnel, from the accounts themselves: a
// device's row is its files one per tunnel, and the columns add up to
// the customer's total because the accounts are all there is.
const usageMatrix = computed(() => {
  const accounts = client.value?.accounts || []
  const devices = []
  const ifaceIds = []
  for (const a of accounts) {
    if (!devices.includes(a.deviceName)) devices.push(a.deviceName)
    if (!ifaceIds.includes(a.interfaceId)) ifaceIds.push(a.interfaceId)
  }
  const cell = (device, ifaceId) => {
    const a = accounts.find((x) => x.deviceName === device && x.interfaceId === ifaceId)
    return a ? (a.upBytes || 0) + (a.downBytes || 0) : null
  }
  const rows = devices.map((device) => {
    const cells = ifaceIds.map((id) => cell(device, id))
    return { device, cells, total: cells.reduce((s, v) => s + (v || 0), 0) }
  })
  const colTotals = ifaceIds.map((_, i) => rows.reduce((s, r) => s + (r.cells[i] || 0), 0))
  return { devices: rows, ifaceIds, colTotals, grand: rows.reduce((s, r) => s + r.total, 0) }
})

// Every device's configuration in one archive.
//
// Fetched with the session token rather than linked: these are private keys,
// and a plain link carries no Authorization header — it would either fail or
// force the panel to serve them unauthenticated.
async function downloadAll() {
  downloading.value = true
  try {
    // The server names the file after the customer; the id is the fallback.
    await saveFile(`/api/clients/${props.id}/configs`, `client-${props.id}.zip`)
  } catch (e) {
    notify(e.message, 'error')
  } finally {
    downloading.value = false
  }
}

const savingProfile = ref(false)

async function saveProfile() {
  savingProfile.value = true
  try {
    await api.downloadProfile(profile.value.account.id)
  } catch (e) {
    notify(e.message, 'error')
  } finally {
    savingProfile.value = false
  }
}

async function load() {
  loading.value = true
  try {
    client.value = await api.client(props.id)
  } catch (err) {
    notify(err.message, 'error')
  } finally {
    loading.value = false
  }
}

onMounted(() => {
  load()
  loadSub()
  api.interfaces({ background: true }).then((list) => (interfaces.value = list || [])).catch(() => {})
})

async function showProfile(account) {
  try {
    const p = await api.profile(account.id)
    profile.value = { ...p, account }
    qr.value = ''
    // Opening another device is not asking to see its key.
    showConfig.value = false
    // WireGuard clients import a tunnel by camera, so the QR is the primary
    // delivery path on mobile. OpenVPN clients cannot, so it is skipped there.
    if (client.value.protocol === 'wireguard') {
      const code = await makeQR(p.body)
      qr.value = code.dataUrl
      qrSize.value = code.size
    }
  } catch (err) {
    notify(err.message, 'error')
  }
}

async function guard(fn, successKey) {
  try {
    const result = await fn()
    if (successKey) notify(t(successKey), 'success')
    return result
  } catch (err) {
    notify(err.message, 'error')
  }
}

const addDevice = () =>
  guard(async () => {
    await api.addDevice(props.id, newDevice.value)
    newDevice.value = ''
    await load()
  }, 'device.added')

const ask = ref(null)

const removeDevice = (account) => {
  ask.value = {
    title: t('device.confirmRemoveTitle'),
    body: t('device.confirmRemoveBody'),
    subject: account.deviceName,
    // Both worth saying: the customer's file stops working, and the address it
    // held goes back to the pool and may be handed to somebody else.
    consequences: [t('device.consequenceConfig'), t('device.consequenceAddress')],
    confirmLabel: t('action.delete'),
    run: () =>
      guard(async () => {
        await api.removeDevice(account.id)
        await load()
      }, 'device.removed'),
  }
}

async function runConfirmed() {
  const spec = ask.value
  if (!spec) return
  busy.value = true
  try {
    await spec.run()
  } finally {
    busy.value = false
    ask.value = null
  }
}

const setStatus = (status) =>
  guard(async () => {
    client.value = await api.updateClient(props.id, { status })
  }, 'client.updated')

const resetTraffic = () =>
  guard(async () => {
    client.value = await api.resetTraffic(props.id)
  }, 'client.trafficReset')

const remove = () => {
  const c = client.value
  ask.value = {
    title: t('client.confirmDeleteTitle'),
    body: t('client.confirmDeleteBody'),
    subject: c?.name || '',
    consequences: [
      tn('client.consequenceDevices', deviceCount.value),
      t('client.consequenceConfigs'),
      t('client.consequenceUsage'),
    ],
    confirmLabel: t('action.delete'),
    run: () =>
      guard(async () => {
        await api.deleteClient(props.id)
        router.push('/clients')
      }, 'client.deleted'),
  }
}

async function copy(text) {
  try {
    await navigator.clipboard.writeText(text)
    notify(t('action.copied'), 'success')
  } catch {
    notify(t('action.copyFailed'), 'error')
  }
}
// An L2TP file is the sign-in written out for every device: nothing worth
// revealing in the panel, where the setup card shows one device at a time.
const isL2TP = (p) => /-(l2tp|pptp)\.txt$/.test(p.filename || '')
</script>

<template>
  <!-- The page's own shape: the customer's name, the plan figures across, then
       a row for each device. A spinner in a short card made the page jump to
       full height the moment it was replaced. -->
  <template v-if="showSkeleton">
    <div class="page-head" aria-hidden="true">
      <div>
        <span class="sk" style="width: 90px"></span>
        <span class="sk sk-lg" style="width: 190px"></span>
      </div>
    </div>
    <section class="card sk-block" aria-hidden="true">
      <div class="sk-stats">
        <span v-for="n in 4" :key="n" class="sk sk-lg"></span>
      </div>
    </section>
    <section class="card sk-rows" aria-hidden="true">
      <div v-for="n in 3" :key="n" class="sk-row">
        <div class="sk-row-meta">
          <span class="sk" style="width: 42%"></span>
          <span class="sk" style="width: 68%"></span>
        </div>
        <span class="sk sk-lg sk-row-control"></span>
      </div>
    </section>
  </template>

  <template v-else-if="client">
    <div class="cs">
      <div class="row mb">
        <RouterLink to="/clients" class="btn ghost sm">‹ {{ t('nav.clients') }}</RouterLink>
      </div>

      <!-- The header: who this is, how their plan stands, and the three
           things that can be done to the whole customer at once. -->
      <div class="card mb2">
        <div class="cb row" style="gap: 18px">
          <span class="avatar" style="width: 56px; height: 56px; font-size: 22px; border-radius: 16px">
            {{ (client.name || '?').slice(0, 1) }}
          </span>
          <div class="col">
            <div class="row tight">
              <h2 style="font-size: 24px; font-weight: 800">{{ client.name }}</h2>
              <span class="dtag" :class="statusTag.tone">
                <span v-if="statusTag.dot" class="d"></span>{{ statusTag.label }}
              </span>
              <span v-if="client.group" class="dtag acc">{{ client.group }}</span>
              <span class="dtag gray">{{ client.protocol }}</span>
            </div>
            <span class="faint small">{{ metaLine }}</span>
          </div>
          <div class="row tight spacer">
            <button v-if="client.status === 'active'" class="btn sm" @click="setStatus('disabled')">
              <Icon name="power" :size="13" />{{ t('action.disable') }}
            </button>
            <button v-else class="btn sm" @click="setStatus('active')">
              <Icon name="power" :size="13" />{{ t('action.enable') }}
            </button>
            <button class="btn sm" @click="resetTraffic">
              <Icon name="refresh" :size="13" />{{ t('action.resetTraffic') }}
            </button>
            <button class="btn sm danger" @click="remove">
              <Icon name="trash" :size="13" />{{ t('action.delete') }}
            </button>
          </div>
        </div>
      </div>

      <div class="grid g12">
        <!-- The plan: the ring is the allowance, the figures beside it
             are what is left of it and of the time. -->
        <section class="card s5">
          <div class="ch">
            <h3>{{ t('client.plan') }}</h3>
            <span class="dtag spacer" :class="statusTag.tone">{{ statusTag.label }}</span>
          </div>
          <div class="cb row nowrap" style="gap: 24px">
            <RingGauge :percent="percent(client.usedBytes, client.quotaBytes) ?? 0" :size="148" :thickness="10" />
            <dl class="kv" style="flex: 1">
              <dt>{{ t('client.used') }}</dt>
              <dd>{{ bytes(client.usedBytes, store.locale) }}</dd>
              <dt>{{ t('client.quota') }}</dt>
              <dd>{{ client.quotaBytes ? bytes(client.quotaBytes, store.locale) : t('client.unlimited') }}</dd>
              <dt v-if="client.quotaBytes">{{ t('client.remaining') }}</dt>
              <dd v-if="client.quotaBytes" class="ok-t">
                <b>{{ bytes(Math.max(0, client.quotaBytes - client.usedBytes), store.locale) }}</b>
              </dd>
              <dt>{{ t('client.timeLeft') }}</dt>
              <!-- A plan waiting for its first connection has no date yet. Saying
                   "never expires" here would be the opposite of the truth about a
                   plan with a fixed length. -->
              <dd v-if="!client.expiresAt && client.startOnFirstUse && client.durationDays > 0">
                <span class="dtag info ltr">{{ client.durationDays }}d</span>
                <span class="muted small">{{ t('client.notStartedHint') }}</span>
              </dd>
              <dd v-else>{{ client.expiresAt ? relative(client.expiresAt, store.locale) : '∞' }}</dd>
              <dt v-if="client.expiresAt">{{ t('client.expires') }}</dt>
              <dd v-if="client.expiresAt">{{ dateTime(client.expiresAt, store.locale) }}</dd>
              <dt>{{ t('client.resetCycle') }}</dt>
              <dd>{{ t(`reset.${client.resetCycle}`) }}</dd>
              <dt>{{ t('client.deviceLimit') }}</dt>
              <dd class="ltr">{{ deviceCount }} / {{ client.deviceLimit || '∞' }}</dd>
              <!-- Shown only once there is a split to show. The enforcer that
                   cannot tell the directions apart leaves both at zero, and
                   "↓ 0 B ↑ 0 B" under a real total reads as a fault. -->
              <dt v-if="client.downBytes || client.upBytes">{{ t('overview.upload') }} / {{ t('overview.download') }}</dt>
              <dd v-if="client.downBytes || client.upBytes" class="mono">
                ↑ {{ bytes(client.upBytes, store.locale) }} · ↓ {{ bytes(client.downBytes, store.locale) }}
              </dd>
            </dl>
          </div>
        </section>

        <!-- The one link that carries every server, with its code. -->
        <section class="card s7">
          <div class="ch">
            <h3>{{ t('client.subscription') }}</h3>
            <span class="sub">{{ t('client.subLine') }}</span>
          </div>
          <div class="cb row nowrap" style="gap: 22px; align-items: flex-start">
            <div v-if="subQr" class="qrbox">
              <img :src="subQr" :alt="t('client.subscription')" :width="subQrSize" :height="subQrSize" />
            </div>
            <div class="col" style="flex: 1; gap: 12px">
              <div v-if="subLink" class="link-box">
                <span>{{ subLink }}</span>
                <button class="btn xs" @click="copy(subLink)">{{ t('action.copy') }}</button>
                <a class="btn xs" :href="subLink" target="_blank" rel="noopener">{{ t('overview.openPage') }}</a>
              </div>
              <p v-else class="muted small">{{ t('client.subUnavailable') }}</p>
            </div>
          </div>
        </section>

        <!-- The files, one row per account: a device on three tunnels is
             three files, and each is configured and removed on its own. -->
        <section class="card calm s12">
          <div class="ch">
            <h3>{{ t('device.title') }}</h3>
            <span class="sub">
              {{ (client.accounts || []).length }} {{ t('device.filesWord') }} · {{ deviceCount }} / {{ client.deviceLimit || '∞' }}
            </span>
            <div class="row tight spacer">
              <input
                v-model="newDevice"
                :placeholder="t('device.namePlaceholder')"
                style="max-width: 170px"
                @keyup.enter="addDevice"
              />
              <button
                class="btn sm primary"
                :disabled="deviceCount >= 64"
                :title="deviceCount >= 64 ? t('error.deviceLimitReached') : ''"
                @click="addDevice"
              >
                <Icon name="plus" :size="13" />{{ t('device.add') }}
              </button>
              <!-- A .conf holds one [Interface], so several devices cannot be one
                   file. Shown only when there is more than one to collect. -->
              <button
                v-if="(client.accounts?.length ?? 0) > 1"
                class="btn sm"
                :title="t('device.downloadAllHint')"
                :disabled="downloading"
                @click="downloadAll"
              >
                <span v-if="downloading" class="spin sm"></span>
                <Icon v-else name="download" :size="13" />{{ t('device.downloadAll') }}
              </button>
            </div>
          </div>

          <div v-if="!client.accounts?.length" class="dempty">{{ t('device.none') }}</div>

          <div v-else class="tw">
            <table>
              <thead>
                <tr>
                  <th>{{ t('device.name') }}</th>
                  <th>{{ t('nav.interfaces') }}</th>
                  <th>{{ t('device.address') }}</th>
                  <th>{{ t('client.status') }}</th>
                  <th>{{ t('client.used') }}</th>
                  <th>{{ t('device.lastSeen') }}</th>
                  <th></th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="acc in client.accounts" :key="acc.id">
                  <td>
                    <div class="col">
                      <b class="mono">{{ acc.deviceName }}</b>
                      <span v-if="acc.username" class="faint xs">{{ acc.username }}</span>
                    </div>
                  </td>
                  <td><span class="dtag sq gold">{{ ifaceName(acc.interfaceId) }}</span></td>
                  <td class="mono">{{ acc.ip }}</td>
                  <td>
                    <span class="dtag" :class="isOnline(acc.lastHandshake) ? 'ok' : 'gray'">
                      <span v-if="isOnline(acc.lastHandshake)" class="d"></span>
                      {{ isOnline(acc.lastHandshake) ? t('status.online') : t('status.offline') }}
                    </span>
                  </td>
                  <td class="mono">{{ bytes((acc.upBytes || 0) + (acc.downBytes || 0), store.locale) }}</td>
                  <td class="small muted">
                    {{ acc.lastHandshake ? relative(acc.lastHandshake, store.locale) : t('node.never') }}
                  </td>
                  <td class="w0">
                    <div class="acts">
                      <button class="act" :title="t('device.config')" :aria-label="t('device.config')" @click="showProfile(acc)">
                        <Icon name="qr" :size="15" />
                      </button>
                      <button
                        class="act danger"
                        :title="t('action.delete')"
                        :aria-label="t('action.delete')"
                        @click="removeDevice(acc)"
                      >
                        <Icon name="trash" :size="15" />
                      </button>
                    </div>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
        </section>

        <!-- What each device moved on each tunnel, from the accounts:
             the rows and columns add up to the plan's total. -->
        <section v-if="(client.accounts || []).length" class="card calm s12">
          <div class="ch">
            <h3>{{ t('client.usageByTunnel') }}</h3>
          </div>
          <div class="tw">
            <table>
              <thead>
                <tr>
                  <th>{{ t('device.name') }}</th>
                  <th v-for="id in usageMatrix.ifaceIds" :key="id">{{ ifaceName(id) }}</th>
                  <th>{{ t('overview.totalData') }}</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="row in usageMatrix.devices" :key="row.device">
                  <td class="mono">{{ row.device }}</td>
                  <td v-for="(v, i) in row.cells" :key="i" class="mono">
                    {{ v === null ? '—' : bytes(v, store.locale) }}
                  </td>
                  <td class="mono"><b>{{ bytes(row.total, store.locale) }}</b></td>
                </tr>
                <tr>
                  <td><b>{{ t('overview.totalData') }}</b></td>
                  <td v-for="(v, i) in usageMatrix.colTotals" :key="i" class="mono"><b>{{ bytes(v, store.locale) }}</b></td>
                  <td class="mono"><b class="accent-t">{{ bytes(usageMatrix.grand, store.locale) }}</b></td>
                </tr>
              </tbody>
            </table>
          </div>
        </section>
      </div>
    </div>

    <div v-if="profile" class="modal-backdrop" @click.self="profile = null">
      <div class="modal" role="dialog" aria-modal="true">
        <div class="card-head">
          <h2>{{ profile.account.deviceName }}</h2>
          <span class="mono small muted">{{ profile.filename }}</span>
          <button class="btn sm icon ghost spacer" :aria-label="t('action.cancel')" @click="profile = null">
            <Icon name="close" :size="14" />
          </button>
        </div>

        <div class="card-body">
          <div v-if="qr" class="qr">
            <img :src="qr" :alt="t('device.showQR')" :width="qrSize" :height="qrSize" />
            <p class="muted small">{{ t('device.qrHint') }}</p>
          </div>

          <div v-if="profile.username" class="creds">
            <div class="field">
              <label>{{ t('auth.username') }}</label>
              <div class="cred">
                <code>{{ profile.username }}</code>
                <button class="btn sm icon" :aria-label="t('action.copy')" @click="copy(profile.username)">
                  <Icon name="copy" :size="13" />
                </button>
              </div>
            </div>
            <div class="field">
              <label>{{ t('auth.password') }}</label>
              <div class="cred">
                <code>{{ profile.secret }}</code>
                <button class="btn sm icon" :aria-label="t('action.copy')" @click="copy(profile.secret)">
                  <Icon name="copy" :size="13" />
                </button>
              </div>
            </div>
          </div>

            <!-- Not on screen unless somebody deliberately asks for it.
                 The body is a private key with some routing around it: it is
                 handed over by QR or by the download button, and putting it in
                 front of whoever is looking at the panel -- over a shoulder, in
                 a screen share, in a screenshot of something else -- gives away
                 the tunnel to anyone who can read it. -->
            <div v-if="!isL2TP(profile)" class="secret">
              <button class="btn sm ghost" @click="showConfig = !showConfig">
                <Icon :name="showConfig ? 'eyeOff' : 'eye'" :size="14" />
                {{ showConfig ? t('device.hideConfig') : t('device.revealConfig') }}
              </button>
              <p v-if="!showConfig" class="muted small">{{ t('device.configHidden') }}</p>
              <pre v-else class="config">{{ profile.body }}</pre>
            </div>
            <!-- This file on the customer's own device: phone, computer or router. -->
            <DeviceSetup v-if="profile.guides && profile.guides.length" :files="[{ key: profile.filename, label: profile.account.deviceName, guides: profile.guides }]" />
        </div>

        <div class="modal-foot">
          <button class="btn ghost" @click="copy(profile.body)">
            <Icon name="copy" :size="13" />{{ t('action.copy') }}
          </button>
          <button class="btn primary" :disabled="savingProfile" @click="saveProfile">
            <span v-if="savingProfile" class="spin sm"></span>
            <Icon v-else name="download" :size="13" />{{ t('device.downloadConfig') }}
          </button>
        </div>
      </div>
    </div>
  </template>

  <ConfirmDialog
    :open="!!ask"
    :title="ask?.title || ''"
    :body="ask?.body || ''"
    :subject="ask?.subject || ''"
    :consequences="ask?.consequences || []"
    :confirm-label="ask?.confirmLabel || ''"
    :busy="busy"
    @confirm="runConfirmed"
    @cancel="ask = null"
  />
</template>

<style scoped>
.card-body {
  display: flex;
  flex-direction: column;
  gap: 14px;
}
.secret {
  display: flex;
  flex-direction: column;
  align-items: flex-start;
  gap: 8px;
}
.qr {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 8px;
}
.qr img {
  border-radius: var(--radius-sm);
  /* Deliberately white in every theme. A scanner reads the contrast between
     light and dark modules, and tinting the light ones is what makes a code
     fail on half the phones that try it. */
  background: #ffffff;
  padding: 8px;
  /* The white border sits around the image rather than being taken out of it:
     the code is sized to whole pixels per module and must not be squeezed. */
  box-sizing: content-box;
  max-width: 100%;
  height: auto;
}
.creds {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(190px, 1fr));
  gap: 12px;
}
.cred {
  display: flex;
  align-items: center;
  gap: 6px;
}
.cred code {
  flex: 1;
  min-width: 0;
  background: var(--surface-2);
  border: 1px solid var(--line);
  border-radius: var(--radius-sm);
  padding: 6px 9px;
  overflow-x: auto;
  white-space: nowrap;
}
</style>
