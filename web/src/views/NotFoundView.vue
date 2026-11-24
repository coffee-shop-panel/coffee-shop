<script setup>
import { useRoute } from 'vue-router'
import { RouterLink } from 'vue-router'
import { t } from '../lib/store.js'
import Icon from '../components/Icon.vue'

// Unknown addresses used to be redirected to the overview, which quietly
// swallowed a typo and left the operator wondering whether they had clicked
// something wrong or the page had moved.
const route = useRoute()
</script>

<template>
  <div class="cs">
    <section class="card">
      <div class="dempty">
        <span class="ico-tile acc"><Icon name="search" :size="24" /></span>
        <div class="hero-num" style="font-size: 64px">404</div>
        <h2>{{ t('error.noSuchPage') }}</h2>
        <p class="mono" dir="ltr">{{ route.fullPath }}</p>
        <div class="row tight" style="justify-content: center">
          <RouterLink to="/" class="btn primary">{{ t('nav.overview') }}</RouterLink>
          <RouterLink to="/clients" class="btn">{{ t('nav.clients') }}</RouterLink>
        </div>
      </div>
    </section>
  </div>
</template>
