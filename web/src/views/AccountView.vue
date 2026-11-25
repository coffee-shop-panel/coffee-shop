<script setup>
// The operator's own page: who they are, and the keys to their own
// sign-in. Every signed-in operator has it -- owner, administrator and
// reseller alike -- because a passkey, and a second factor, are enrolled
// by the person who will use them, and the only page holding those
// controls sat inside Settings, which the menu keeps for the owner
// alone. The sections are the settings page's own, shared rather than
// copied, so a key enrolled here and one enrolled there are one list.
//
// Nothing else lives here on purpose: this page is not a second settings
// page, and it offers nothing the panel does not already offer the
// operator somewhere.
import { computed } from 'vue'
import { store, t } from '../lib/store.js'
import AntIcon from '../components/AntIcon.vue'
import PasskeysSection from '../components/settings/PasskeysSection.vue'
import TwoFactorSection from '../components/settings/TwoFactorSection.vue'

const me = computed(() => store.admin)
// The role's name, from the same words the operators page files people
// under, so the page calls the operator what the panel calls them.
const roleLabel = computed(() => (me.value?.role ? t(`admins.role.${me.value.role}`) : ''))
</script>

<template>
  <div class="cs">
    <div class="ph">
      <div>
        <h2>{{ t('nav.account') }}</h2>
        <p>{{ t('account.lede') }}</p>
      </div>
    </div>

    <section class="card calm account-card">
      <div class="account-id">
        <span class="account-avatar"><AntIcon name="UserOutlined" /></span>
        <div class="account-id-meta">
          <span class="account-name">{{ me?.username }}</span>
          <span v-if="roleLabel" class="dtag info">{{ roleLabel }}</span>
        </div>
      </div>
    </section>

    <section class="card calm account-card">
      <div class="card-head"><h2>{{ t('settings.tab.security') }}</h2></div>
      <TwoFactorSection />
      <PasskeysSection />
    </section>
  </div>
</template>

<style scoped>
.account-card { margin-bottom: 18px; }
.account-id { display: flex; align-items: center; gap: 14px; padding: 18px; }
.account-avatar {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 44px;
  height: 44px;
  border-radius: 50%;
  background: var(--accent-soft);
  color: var(--accent-text);
  font-size: 20px;
  flex: none;
}
.account-id-meta { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; min-width: 0; }
.account-name { font-size: 17px; font-weight: 700; letter-spacing: -0.01em; }
</style>
