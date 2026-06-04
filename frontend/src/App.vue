<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'

type DocumentSummary = {
  id: number
  filename: string
  createdAt: string
}

type DocumentRecord = DocumentSummary & {
  content: string
}

type SelectionRange = {
  start: number
  end: number
}

type ReaderPart =
  | { kind: 'word'; index: number; text: string }
  | { kind: 'space'; text: string }

type ViewMode = 'library' | 'reader'
type TranslationItem = {
  key: string
  text: string
  translation: string
}

type TranslationGroup = SelectionRange & {
  key: string
  text: string
  paragraphIndex: number
}

type ParagraphSegment =
  | { type: 'plain'; key: string; parts: ReaderPart[] }
  | { type: 'group'; key: string; parts: ReaderPart[]; translation: string }

type TranslationCache = Record<string, string>

const documents = ref<DocumentSummary[]>([])
const activeDocument = ref<DocumentRecord | null>(null)
const viewMode = ref<ViewMode>('library')
const currentPageIndex = ref(0)
const readerPages = ref<ReaderPart[][][]>([])
const loading = ref(false)
const uploading = ref(false)
const errorMessage = ref('')
const selectedRanges = ref<SelectionRange[]>([])
const previewRange = ref<SelectionRange | null>(null)
const hoveredWordIndex = ref<number | null>(null)
const selectionStartIndex = ref<number | null>(null)
const selectionEndIndex = ref<number | null>(null)
const isSelecting = ref(false)
const readerMeasureRef = ref<HTMLElement | null>(null)
const translations = ref<TranslationItem[]>([])
const translationError = ref('')
const translating = ref(false)
const translationCache = ref<Record<number, TranslationCache>>({})
let translationRequestVersion = 0

const parsedParagraphs = computed(() => {
  if (!activeDocument.value) {
    return [] as ReaderPart[][]
  }

  let wordIndex = 0
  return activeDocument.value.content
    .split(/\n\s*\n/g)
    .map((paragraph) => paragraph.trim())
    .filter(Boolean)
    .map((paragraph) => {
      const parts: ReaderPart[] = []

      for (const token of paragraph.split(/(\s+)/)) {
        if (!token) {
          continue
        }

        if (/^\s+$/.test(token)) {
          parts.push({ kind: 'space', text: token })
          continue
        }

        parts.push({ kind: 'word', index: wordIndex, text: token })
        wordIndex += 1
      }

      return parts
    })
})

const currentPage = computed(() => readerPages.value[currentPageIndex.value] ?? [])

const totalPages = computed(() => readerPages.value.length)
const translationMap = computed(() => new Map(translations.value.map((item) => [item.key, item.translation])))

const wordLookup = computed(() => {
  const entries = parsedParagraphs.value
    .flatMap((paragraph) => paragraph)
    .filter((part): part is Extract<ReaderPart, { kind: 'word' }> => part.kind === 'word')
    .map((word) => [word.index, word.text] as const)

  return new Map<number, string>(entries)
})

const currentPageTranslationGroups = computed(() => {
  return currentPage.value.flatMap((paragraph, paragraphIndex) => {
    const words = paragraph.filter((part): part is Extract<ReaderPart, { kind: 'word' }> => part.kind === 'word')
    if (words.length === 0) {
      return [] as TranslationGroup[]
    }

    const bounds = {
      start: words[0].index,
      end: words[words.length - 1].index,
    }

    const visibleRanges = selectedRanges.value
      .map((range) => intersectRanges(range, bounds))
      .filter((range): range is SelectionRange => range !== null)

    if (visibleRanges.length === 0) {
      return [] as TranslationGroup[]
    }

    return visibleRanges
      .map((range) => ({
        ...range,
        paragraphIndex,
        key: `${paragraphIndex}:${range.start}:${range.end}`,
        text: rangeToText(range),
      }))
      .filter((group) => group.text !== '')
  })
})

const currentPageSegments = computed(() => {
  return currentPage.value.map((paragraph, paragraphIndex) => {
    const groups = currentPageTranslationGroups.value.filter((group) => group.paragraphIndex === paragraphIndex)
    return buildParagraphSegments(paragraph, paragraphIndex, groups)
  })
})

onMounted(async () => {
  window.addEventListener('mouseup', finalizeSelection)
  window.addEventListener('resize', handleViewportChange)
  await loadDocuments()
})

onBeforeUnmount(() => {
  window.removeEventListener('mouseup', finalizeSelection)
  window.removeEventListener('resize', handleViewportChange)
})

watch(parsedParagraphs, async () => {
  await paginateReader()
})

watch(viewMode, async (mode) => {
  if (mode === 'reader') {
    await paginateReader()
  }
})

watch(currentPageTranslationGroups, async (groups) => {
  await syncTranslations(groups)
}, { deep: true })

async function loadDocuments() {
  loading.value = true
  errorMessage.value = ''

  try {
    const response = await fetch('/api/documents')
    if (!response.ok) {
      throw new Error('Failed to load documents')
    }

    documents.value = await response.json()
  } catch (error) {
    errorMessage.value = error instanceof Error ? error.message : 'Unknown error'
  } finally {
    loading.value = false
  }
}

async function openDocument(id: number) {
  errorMessage.value = ''
  resetReaderState()

  try {
    const response = await fetch(`/api/documents/${id}`)
    if (!response.ok) {
      throw new Error('Failed to load document')
    }

    activeDocument.value = await response.json()
    viewMode.value = 'reader'
    await paginateReader()
  } catch (error) {
    errorMessage.value = error instanceof Error ? error.message : 'Unknown error'
  }
}

async function uploadDocument(event: Event) {
  const input = event.target as HTMLInputElement
  const file = input.files?.[0]
  if (!file) {
    return
  }

  const formData = new FormData()
  formData.append('document', file)

  uploading.value = true
  errorMessage.value = ''

  try {
    const response = await fetch('/api/documents', {
      method: 'POST',
      body: formData,
    })

    const data = await response.json()
    if (!response.ok) {
      throw new Error(data.error ?? 'Upload failed')
    }

    await loadDocuments()
    activeDocument.value = data
    resetReaderState()
    viewMode.value = 'reader'
    await paginateReader()
  } catch (error) {
    errorMessage.value = error instanceof Error ? error.message : 'Unknown error'
  } finally {
    uploading.value = false
    input.value = ''
  }
}

async function syncTranslations(groups: Array<{ key: string; text: string }>) {
  translationRequestVersion += 1
  const requestVersion = translationRequestVersion
  const documentID = activeDocument.value?.id

  translationError.value = ''

  if (groups.length === 0) {
    translations.value = []
    translating.value = false
    return
  }

  const cache = documentID ? (translationCache.value[documentID] ??= {}) : {}
  const cachedItems = groups
    .map((group) => ({
      key: group.key,
      text: group.text,
      translation: cache[translationCacheKey(group.text)] ?? '',
    }))

  const missingGroups = groups.filter((group) => !cache[translationCacheKey(group.text)])
  if (missingGroups.length === 0) {
    translations.value = cachedItems
    translating.value = false
    return
  }

  translations.value = cachedItems
  translating.value = true

  try {
    const response = await fetch('/api/translations', {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
      },
      body: JSON.stringify({
        texts: missingGroups.map((group) => group.text),
        targetLanguage: 'English',
      }),
    })

    const data = await response.json()
    if (!response.ok) {
      throw new Error(data.error ?? 'Translation failed')
    }

    if (requestVersion !== translationRequestVersion) {
      return
    }

    for (const [index, group] of missingGroups.entries()) {
      const translation = data.translations?.[index]?.translation ?? ''
      if (documentID && translation) {
        cache[translationCacheKey(group.text)] = translation
      }
    }

    translations.value = groups.map((group) => ({
      key: group.key,
      text: group.text,
      translation: cache[translationCacheKey(group.text)] ?? '',
    }))
  } catch (error) {
    if (requestVersion !== translationRequestVersion) {
      return
    }

    translationError.value = error instanceof Error ? error.message : 'Unknown translation error'
  } finally {
    if (requestVersion === translationRequestVersion) {
      translating.value = false
    }
  }
}

function goToPreviousPage() {
  if (currentPageIndex.value > 0) {
    currentPageIndex.value -= 1
  }
}

function goToNextPage() {
  if (currentPageIndex.value < totalPages.value - 1) {
    currentPageIndex.value += 1
  }
}

async function handleViewportChange() {
  if (viewMode.value !== 'reader' || !activeDocument.value) {
    return
  }

  await paginateReader()
}

function formatDate(value: string) {
  return new Date(value).toLocaleDateString()
}

function handleWordMouseDown(index: number, event: MouseEvent) {
  event.preventDefault()
  clearNativeSelection()
  isSelecting.value = true
  selectionStartIndex.value = index
  selectionEndIndex.value = index
  previewRange.value = normalizeRange(index, index)
}

function handleWordMouseEnter(index: number) {
  hoveredWordIndex.value = index

  if (!isSelecting.value || selectionStartIndex.value === null) {
    return
  }

  selectionEndIndex.value = index
  previewRange.value = normalizeRange(selectionStartIndex.value, index)
}

function handleWordMouseLeave(index: number) {
  if (hoveredWordIndex.value === index) {
    hoveredWordIndex.value = null
  }
}

function finalizeSelection() {
  if (!isSelecting.value || selectionStartIndex.value === null || selectionEndIndex.value === null) {
    return
  }

  const nextRange = normalizeRange(selectionStartIndex.value, selectionEndIndex.value)
  const existingRange = findRangeForWord(selectionStartIndex.value)
  const clickedSelectedWord = selectionStartIndex.value === selectionEndIndex.value && existingRange !== null

  if (clickedSelectedWord) {
    selectedRanges.value = selectedRanges.value.filter((range) => !rangesEqual(range, existingRange))
  } else {
    selectedRanges.value = addRange(selectedRanges.value, nextRange)
  }

  isSelecting.value = false
  previewRange.value = null
  selectionStartIndex.value = null
  selectionEndIndex.value = null
  clearNativeSelection()
}

function isWordSelected(index: number) {
  return isIndexHighlighted(index, previewRange.value)
}

function isWordRemovable(index: number) {
  return !isSelecting.value && hoveredWordIndex.value === index && findRangeForWord(index) !== null
}

function resetReaderState() {
  currentPageIndex.value = 0
  readerPages.value = []
  selectedRanges.value = []
  previewRange.value = null
  hoveredWordIndex.value = null
  selectionStartIndex.value = null
  selectionEndIndex.value = null
  isSelecting.value = false
  translations.value = []
  translationError.value = ''
  translating.value = false
}

function normalizeRange(start: number, end: number): SelectionRange {
  return start <= end ? { start, end } : { start: end, end: start }
}

function isIndexInRange(index: number, range: SelectionRange) {
  return index >= range.start && index <= range.end
}

function rangesEqual(a: SelectionRange | null, b: SelectionRange | null) {
  return a?.start === b?.start && a?.end === b?.end
}

function isIndexHighlighted(index: number, transientRange: SelectionRange | null) {
  if (transientRange && isIndexInRange(index, transientRange)) {
    return true
  }

  return findRangeForWord(index) !== null
}

function findRangeForWord(index: number) {
  return selectedRanges.value.find((range) => isIndexInRange(index, range)) ?? null
}

function addRange(ranges: SelectionRange[], nextRange: SelectionRange) {
  let mergedRange = nextRange
  const remainingRanges: SelectionRange[] = []

  for (const range of ranges) {
    if (rangesOverlapOrTouch(range, mergedRange)) {
      mergedRange = {
        start: Math.min(range.start, mergedRange.start),
        end: Math.max(range.end, mergedRange.end),
      }
      continue
    }

    remainingRanges.push(range)
  }

  return [...remainingRanges, mergedRange].sort((a, b) => a.start - b.start)
}

function rangesOverlapOrTouch(a: SelectionRange, b: SelectionRange) {
  return a.start <= b.end + 1 && b.start <= a.end + 1
}

function clearNativeSelection() {
  window.getSelection()?.removeAllRanges()
}

function intersectRanges(a: SelectionRange, b: SelectionRange) {
  const start = Math.max(a.start, b.start)
  const end = Math.min(a.end, b.end)

  if (start > end) {
    return null
  }

  return { start, end }
}

function rangeToText(range: SelectionRange) {
  const words: string[] = []

  for (let index = range.start; index <= range.end; index += 1) {
    const word = wordLookup.value.get(index)
    if (word) {
      words.push(word)
    }
  }

  return words.join(' ')
}

function translationCacheKey(text: string) {
  return `English:${text}`
}

function buildParagraphSegments(paragraph: ReaderPart[], paragraphIndex: number, groups: TranslationGroup[]): ParagraphSegment[] {
  if (groups.length === 0) {
    return [{ type: 'plain', key: `${paragraphIndex}-plain-0`, parts: paragraph }]
  }

  const segments: ParagraphSegment[] = []
  let cursor = 0

  for (const group of groups) {
    const startIndex = paragraph.findIndex((part) => part.kind === 'word' && part.index === group.start)
    const endIndex = findLastWordPartIndex(paragraph, group.end)
    if (startIndex === -1 || endIndex === -1 || endIndex < startIndex) {
      continue
    }

    if (startIndex > cursor) {
      segments.push({
        type: 'plain',
        key: `${paragraphIndex}-plain-${cursor}`,
        parts: paragraph.slice(cursor, startIndex),
      })
    }

    segments.push({
      type: 'group',
      key: group.key,
      parts: paragraph.slice(startIndex, endIndex + 1),
      translation: translationMap.value.get(group.key) ?? '',
    })
    cursor = endIndex + 1
  }

  if (cursor < paragraph.length) {
    segments.push({
      type: 'plain',
      key: `${paragraphIndex}-plain-${cursor}`,
      parts: paragraph.slice(cursor),
    })
  }

  return segments
}

function findLastWordPartIndex(paragraph: ReaderPart[], wordIndex: number) {
  for (let index = paragraph.length - 1; index >= 0; index -= 1) {
    const part = paragraph[index]
    if (part.kind === 'word' && part.index === wordIndex) {
      return index
    }
  }

  return -1
}

async function paginateReader() {
  if (viewMode.value !== 'reader' || parsedParagraphs.value.length === 0) {
    readerPages.value = parsedParagraphs.value.length === 0 ? [] : readerPages.value
    currentPageIndex.value = 0
    return
  }

  await nextTick()

  const measurer = readerMeasureRef.value
  if (!measurer || measurer.clientHeight === 0) {
    return
  }

  const pages: ReaderPart[][][] = []
  let currentPage: ReaderPart[][] = []

  for (const paragraph of parsedParagraphs.value) {
    let paragraphSlice: ReaderPart[] = []

    for (const part of paragraph) {
      if (paragraphSlice.length === 0 && part.kind === 'space') {
        continue
      }

      const candidateParagraph = [...paragraphSlice, part]
      const candidatePage = [...currentPage, candidateParagraph]

      if (pageFits(measurer, candidatePage)) {
        paragraphSlice = candidateParagraph
        continue
      }

      if (paragraphSlice.length === 0) {
        paragraphSlice = [part]
        continue
      }

      currentPage.push(trimTrailingSpaces(paragraphSlice))
      pages.push(currentPage)
      currentPage = []
      paragraphSlice = part.kind === 'space' ? [] : [part]
    }

    if (paragraphSlice.length > 0) {
      currentPage.push(trimTrailingSpaces(paragraphSlice))
    }
  }

  if (currentPage.length > 0) {
    pages.push(currentPage)
  }

  readerPages.value = pages
  currentPageIndex.value = Math.min(currentPageIndex.value, Math.max(pages.length - 1, 0))
}

function pageFits(measurer: HTMLElement, page: ReaderPart[][]) {
  renderMeasurePage(measurer, page)
  return measurer.scrollHeight <= measurer.clientHeight
}

function renderMeasurePage(measurer: HTMLElement, page: ReaderPart[][]) {
  measurer.replaceChildren()

  for (const paragraph of page) {
    const paragraphNode = document.createElement('p')
    paragraphNode.className = 'reader-paragraph'

    for (const part of paragraph) {
      const span = document.createElement('span')
      span.className = part.kind
      span.textContent = part.text
      paragraphNode.appendChild(span)
    }

    measurer.appendChild(paragraphNode)
  }
}

function trimTrailingSpaces(parts: ReaderPart[]) {
  const nextParts = [...parts]

  while (nextParts.length > 0 && nextParts[nextParts.length - 1].kind === 'space') {
    nextParts.pop()
  }

  return nextParts
}
</script>

<template>
  <main v-if="viewMode === 'library'" class="library-view">
    <section class="upload-stage">
      <p class="eyebrow">OpenRead</p>
      <h1>Upload a document and start reading.</h1>
      <p class="lede">Text and markdown only for now. The reader opens in a dedicated full-page view.</p>

      <label class="upload-card">
        <span>{{ uploading ? 'Uploading...' : 'Choose document' }}</span>
        <input accept=".txt,.md,text/plain,text/markdown" type="file" @change="uploadDocument" />
      </label>

      <p v-if="errorMessage" class="error">{{ errorMessage }}</p>
    </section>

    <section class="library-strip">
      <p class="strip-label">Available documents</p>
      <p v-if="loading" class="muted">Loading documents...</p>
      <p v-else-if="documents.length === 0" class="muted">No documents yet.</p>

      <div v-else class="document-grid">
        <button
          v-for="document in documents"
          :key="document.id"
          class="document-card"
          @click="openDocument(document.id)"
        >
          <strong>{{ document.filename }}</strong>
          <span>{{ formatDate(document.createdAt) }}</span>
        </button>
      </div>
    </section>
  </main>

  <main v-else class="reader-view">
    <section class="reader-surface">
      <article class="reader" @dragstart.prevent>
        <p v-for="(segments, paragraphIndex) in currentPageSegments" :key="paragraphIndex" class="reader-paragraph">
          <template v-for="segment in segments" :key="segment.key">
            <template v-if="segment.type === 'plain'">
              <template v-for="(part, partIndex) in segment.parts" :key="part.kind === 'word' ? part.index : `${segment.key}-${partIndex}`">
                <span
                  v-if="part.kind === 'word'"
                  class="word"
                  :class="{
                    selected: isWordSelected(part.index),
                    removable: isWordRemovable(part.index),
                  }"
                  @mousedown="handleWordMouseDown(part.index, $event)"
                  @mouseenter="handleWordMouseEnter(part.index)"
                  @mouseleave="handleWordMouseLeave(part.index)"
                >
                  {{ part.text }}
                </span>
                <span v-else class="space">{{ part.text }}</span>
              </template>
            </template>
            <ruby v-else class="translation-group">
              <template v-for="(part, partIndex) in segment.parts" :key="part.kind === 'word' ? part.index : `${segment.key}-${partIndex}`">
                <span
                  v-if="part.kind === 'word'"
                  class="word"
                  :class="{
                    selected: isWordSelected(part.index),
                    removable: isWordRemovable(part.index),
                  }"
                  @mousedown="handleWordMouseDown(part.index, $event)"
                  @mouseenter="handleWordMouseEnter(part.index)"
                  @mouseleave="handleWordMouseLeave(part.index)"
                >
                  {{ part.text }}
                </span>
                <span v-else class="space">{{ part.text }}</span>
              </template>
              <rt class="translation-inline">{{ segment.translation || ' ' }}</rt>
            </ruby>
          </template>
        </p>
      </article>
      <article ref="readerMeasureRef" aria-hidden="true" class="reader reader-measure"></article>
    </section>

    <footer class="reader-footer">
      <nav class="pager">
        <button class="pager-button" :disabled="currentPageIndex === 0" @click="goToPreviousPage">
          Previous page
        </button>
        <span class="page-indicator">{{ currentPageIndex + 1 }} / {{ totalPages }}</span>
        <button class="pager-button" :disabled="currentPageIndex >= totalPages - 1" @click="goToNextPage">
          Next page
        </button>
      </nav>
      <p v-if="translating" class="translation-status">Translating selection...</p>
      <p v-else-if="translationError" class="translation-status translation-error">{{ translationError }}</p>
    </footer>
  </main>
</template>
