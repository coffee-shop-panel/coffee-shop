<script setup>
// The groups as a table. The header row stays when there is nothing under
// it, the way their Table keeps its columns and puts the empty state inside
// the body: a table that vanishes when empty makes the page a different
// shape depending on the data.
import { computed } from 'vue'
import { store, t } from '../../lib/store.js'
import { bytes } from '../../lib/format.js'
import { menuFor } from '../../lib/groups/rows.js'
import Icon from '../Icon.vue'
import MoreMenu from '../MoreMenu.vue'

const props = defineProps({ items: { type: Array, required: true } })
// pick(g, key): a row menu choice; view(g): its customers.
defineEmits(['pick', 'rename', 'view'])

const nf = (n) => Number(n || 0).toLocaleString(store.locale)

// The design's traffic meter is drawn against the busiest group on the
// page: the figures are the panel's, the proportion is only a picture of
// them, so it is measured against what is shown rather than a quota no
// group here carries.
const maxUsed = computed(() => Math.max(1, ...props.items.map((g) => g.usedBytes || 0)))
</script>

<template>
  <div class="tw">
    <table>
      <thead>
        <tr>
          <th>{{ t('table.actions') }}</th>
          <th>{{ t('group.name') }}</th>
          <th>{{ t('group.clientCount') }}</th>
          <th>{{ t('overview.upload') }}</th>
          <th>{{ t('overview.download') }}</th>
          <th>{{ t('group.trafficUsed') }}</th>
        </tr>
      </thead>
      <tbody>
        <tr v-if="!items.length">
          <td colspan="6">
            <div class="dempty">
              <span class="ico-tile"><Icon name="tag" :size="22" /></span>
              <div>{{ t('common.nothingYet') }}</div>
            </div>
          </td>
        </tr>
        <tr v-for="g in items" :key="g.name">
          <!-- Two controls, as theirs has: the whole menu behind one
               button, and rename lifted out of it because it is the one an
               operator reaches for most. -->
          <td class="w0">
            <div class="acts">
              <MoreMenu :items="menuFor(g)" compact button-class="act" :label="t('table.actions')" @pick="(key) => $emit('pick', g, key)" />
              <button class="act" :title="t('group.rename')" @click="$emit('rename', g)">
                <Icon name="edit" :size="16" />
              </button>
            </div>
          </td>

          <td>
            <button class="dtag acc lg name-btn" @click="$emit('view', g)">{{ g.name }}</button>
            <div v-if="g.note" class="note" style="margin-top: 4px">{{ g.note }}</div>
          </td>

          <td class="num"><b>{{ nf(g.clients) }}</b></td>
          <td class="mono ok-t">{{ bytes(g.upBytes, store.locale) }}</td>
          <td class="mono sky-t">{{ bytes(g.downBytes, store.locale) }}</td>
          <td style="min-width: 170px">
            <div class="usage">
              <div class="top"><b class="mono">{{ bytes(g.usedBytes, store.locale) }}</b></div>
              <div class="dmeter"><i :style="{ width: Math.round(((g.usedBytes || 0) / maxUsed) * 100) + '%' }"></i></div>
            </div>
          </td>
        </tr>
      </tbody>
    </table>
  </div>
</template>

<style scoped>
/* The group's name is a tag that is also the way to its customers: the
   design's tag, wearing a button's behaviour. */
.name-btn {
  border: 1px solid var(--accent-line);
  cursor: pointer;
  font-family: var(--mono);
}
.name-btn:hover {
  filter: brightness(1.15);
}
</style>
